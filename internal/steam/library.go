package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/csull/go-steam-organizer/internal/models"
)

type ownedGameAPIEntry struct {
	AppID           uint32 `json:"appid"`
	Name            string `json:"name"`
	PlaytimeForever int    `json:"playtime_forever"`
	RtimeLastPlayed int64  `json:"rtime_last_played"`
}

// FetchOwnedGames retrieves the user's owned games and playtime via IPlayerService/GetOwnedGames/v1,
// falling back to Steam Community profile games JSON and dynamicstore userdata if needed.
func (c *Client) FetchOwnedGames(ctx context.Context) ([]models.LibraryItem, error) {
	itemsByApp := make(map[uint32]models.LibraryItem)

	// 1. Try IPlayerService/GetOwnedGames/v1
	if c.session != nil && c.session.SteamID64 != "" {
		params := url.Values{}
		params.Set("steamid", c.session.SteamID64)
		params.Set("include_appinfo", "true")
		params.Set("include_played_free_games", "true")
		params.Set("include_free_sub", "true")
		params.Set("skip_unvetted_apps", "false")
		params.Set("format", "json")

		if body, err := c.GetWebAPI(ctx, "IPlayerService/GetOwnedGames/v1/", params); err == nil {
			var resp struct {
				Response struct {
					GameCount int                 `json:"game_count"`
					Games     []ownedGameAPIEntry `json:"games"`
				} `json:"response"`
			}
			if json.Unmarshal(body, &resp) == nil {
				for _, g := range resp.Response.Games {
					var lastPlayed time.Time
					if g.RtimeLastPlayed > 0 {
						lastPlayed = time.Unix(g.RtimeLastPlayed, 0)
					}
					itemsByApp[g.AppID] = models.LibraryItem{
						AppID:           g.AppID,
						Name:            g.Name,
						PlaytimeMinutes: g.PlaytimeForever,
						LastPlayed:      lastPlayed,
						AcquisitionType: models.AcquisitionUnknown,
					}
				}
			}
		}
	}

	// 2. Fallback / supplement: Steam Community profile games tab
	if len(itemsByApp) == 0 && c.session != nil && c.session.SteamID64 != "" {
		commURL := fmt.Sprintf("https://steamcommunity.com/profiles/%s/games/?tab=all", c.session.SteamID64)
		if body, err := c.GetStore(ctx, commURL); err == nil {
			for _, g := range parseCommunityGamesHTML(string(body)) {
				if _, exists := itemsByApp[g.AppID]; !exists {
					itemsByApp[g.AppID] = g
				}
			}
		}
	}

	// 3. Supplement with dynamicstore/userdata owned apps
	if body, err := c.GetStore(ctx, "/dynamicstore/userdata/"); err == nil {
		var ds struct {
			RgOwnedApps     []uint32 `json:"rgOwnedApps"`
			RgOwnedPackages []uint32 `json:"rgOwnedPackages"`
		}
		if json.Unmarshal(body, &ds) == nil {
			for _, appID := range ds.RgOwnedApps {
				if _, exists := itemsByApp[appID]; !exists {
					itemsByApp[appID] = models.LibraryItem{
						AppID:           appID,
						Name:            fmt.Sprintf("App %d", appID),
						AcquisitionType: models.AcquisitionUnknown,
					}
				}
			}
		}
	}

	result := make([]models.LibraryItem, 0, len(itemsByApp))
	for _, it := range itemsByApp {
		result = append(result, it)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

var rgGamesAttrRegex = regexp.MustCompile(`data-profile-gameslist="([^"]+)"`)
var rgGamesVarRegex = regexp.MustCompile(`var\s+rgGames\s*=\s*(\[.*?\]);`)

func parseCommunityGamesHTML(pageHTML string) []models.LibraryItem {
	var out []models.LibraryItem

	if m := rgGamesVarRegex.FindStringSubmatch(pageHTML); len(m) > 1 {
		var entries []struct {
			AppID       uint32      `json:"appid"`
			Name        string      `json:"name"`
			HoursriForever json.Number `json:"hours_forever"`
			LastPlayed  int64       `json:"last_played"`
		}
		if json.Unmarshal([]byte(m[1]), &entries) == nil {
			for _, e := range entries {
				hours, _ := e.HoursriForever.Float64()
				var lp time.Time
				if e.LastPlayed > 0 {
					lp = time.Unix(e.LastPlayed, 0)
				}
				out = append(out, models.LibraryItem{
					AppID:           e.AppID,
					Name:            e.Name,
					PlaytimeMinutes: int(hours * 60),
					LastPlayed:      lp,
					AcquisitionType: models.AcquisitionUnknown,
				})
			}
		}
	}
	return out
}

// SyncFullLibrary fetches owned games, purchase history, and licenses, then merges them into a unified slice of LibraryItems.
func (c *Client) SyncFullLibrary(ctx context.Context, forceRefresh bool, maxHistoryPages int, logf func(string)) (*LibraryCache, error) {
	if !forceRefresh && c.session != nil {
		if cached, err := LoadLibraryCache(c.session.SteamID64); err == nil && len(cached.Items) > 0 {
			return cached, nil
		}
	}

	if logf != nil {
		logf("Fetching owned games and playtime from Steam...")
	}
	owned, err := c.FetchOwnedGames(ctx)
	if err != nil && logf != nil {
		logf(fmt.Sprintf("Warning: owned games fetch partial error: %v", err))
	}

	if logf != nil {
		logf("Fetching Steam purchase history...")
	}
	purchases, pErr := c.FetchPurchaseHistory(ctx, maxHistoryPages)
	if pErr != nil && logf != nil {
		logf(fmt.Sprintf("Warning: purchase history fetch error: %v", pErr))
	}

	if logf != nil {
		logf("Fetching Steam account licenses and key activations...")
	}
	licenses, lErr := c.FetchAccountLicenses(ctx)
	if lErr != nil && logf != nil {
		logf(fmt.Sprintf("Warning: account licenses fetch error: %v", lErr))
	}

	merged := MergeLibraryWithPurchases(owned, purchases, licenses)

	steamID := ""
	if c.session != nil {
		steamID = c.session.SteamID64
	}
	cache := &LibraryCache{
		SteamID64: steamID,
		UpdatedAt: time.Now(),
		Items:     merged,
		Purchases: purchases,
		Licenses:  licenses,
	}
	_ = SaveLibraryCache(cache)
	return cache, nil
}

// MergeLibraryWithPurchases correlates owned games with purchase history records and license activations.
func MergeLibraryWithPurchases(
	owned []models.LibraryItem,
	purchases []models.PurchaseRecord,
	licenses []models.LicenseRecord,
) []models.LibraryItem {
	// Index purchases by normalized item title
	purchaseByNorm := make(map[string]models.PurchaseRecord)
	for _, p := range purchases {
		for _, itemTitle := range p.Items {
			norm := NormalizeTitle(itemTitle)
			if norm == "" {
				continue
			}
			// Prefer non-refunded or earlier/explicit purchase match if not yet set
			if existing, ok := purchaseByNorm[norm]; !ok || (!existing.IsRefunded && p.IsRefunded) {
				purchaseByNorm[norm] = p
			}
		}
	}

	// Index licenses by normalized item title
	licenseByNorm := make(map[string]models.LicenseRecord)
	for _, lic := range licenses {
		norm := NormalizeTitle(lic.ItemName)
		if norm == "" {
			continue
		}
		if _, ok := licenseByNorm[norm]; !ok {
			licenseByNorm[norm] = lic
		}
	}

	result := make([]models.LibraryItem, len(owned))
	for i, item := range owned {
		norm := NormalizeTitle(item.Name)

		// 1. Check exact or substring match in PurchaseRecords
		if p, ok := findMatchingPurchase(norm, purchaseByNorm, purchases); ok {
			item.PurchaseDate = p.Date
			item.AcquisitionType = p.AcquisitionType
			item.PriceCents = p.PriceCents
			item.PriceDisplay = p.PriceDisplay
			item.IsFree = p.IsFree
			item.IsRefunded = p.IsRefunded
			item.IsBundle = p.IsBundle
			item.MatchedTransaction = p.TransactionID
		}

		// 2. Supplement with LicenseRecords (especially for Retail Keys, Complimentary/Free, Gifts)
		if lic, ok := findMatchingLicense(norm, licenseByNorm, licenses); ok {
			if item.PurchaseDate.IsZero() && !lic.Date.IsZero() {
				item.PurchaseDate = lic.Date
			}
			if item.AcquisitionType == "" || item.AcquisitionType == models.AcquisitionUnknown {
				item.AcquisitionType = lic.AcquisitionType
				if lic.AcquisitionType == models.AcquisitionFree {
					item.IsFree = true
					item.PriceDisplay = "Free"
				} else if lic.AcquisitionType == models.AcquisitionBundle {
					item.IsBundle = true
				}
			}
		}

		if item.AcquisitionType == "" {
			item.AcquisitionType = models.AcquisitionUnknown
		}
		result[i] = item
	}

	return result
}

func findMatchingPurchase(norm string, exactMap map[string]models.PurchaseRecord, all []models.PurchaseRecord) (models.PurchaseRecord, bool) {
	if norm == "" {
		return models.PurchaseRecord{}, false
	}
	if p, ok := exactMap[norm]; ok {
		return p, true
	}
	for _, p := range all {
		for _, rawTitle := range p.Items {
			cand := NormalizeTitle(rawTitle)
			if cand != "" && len(norm) >= 4 && (strings.Contains(cand, norm) || strings.Contains(norm, cand)) {
				return p, true
			}
		}
	}
	return models.PurchaseRecord{}, false
}

func findMatchingLicense(norm string, exactMap map[string]models.LicenseRecord, all []models.LicenseRecord) (models.LicenseRecord, bool) {
	if norm == "" {
		return models.LicenseRecord{}, false
	}
	if lic, ok := exactMap[norm]; ok {
		return lic, true
	}
	for _, lic := range all {
		cand := NormalizeTitle(lic.ItemName)
		if cand != "" && len(norm) >= 4 && (strings.Contains(cand, norm) || strings.Contains(norm, cand)) {
			return lic, true
		}
	}
	return models.LicenseRecord{}, false
}

