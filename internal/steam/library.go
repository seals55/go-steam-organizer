package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
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

	// 3. Supplement with dynamicstore/userdata owned apps (with cache-busting timestamp)
	dsPath := fmt.Sprintf("/dynamicstore/userdata/?v=%d", time.Now().Unix())
	if body, err := c.GetStore(ctx, dsPath); err == nil {
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

	// 4. Enrich from local Steam appcache (packageinfo.vdf & appinfo.vdf) in <0.5s with zero network rate limits
	EnrichFromLocalSteamCache(itemsByApp, "")

	// 5. Fallback bulk-resolve any remaining unnamed items ("App <id>") via IStoreBrowseService/GetItems/v1
	c.bulkEnrichStoreItems(ctx, itemsByApp)

	result := make([]models.LibraryItem, 0, len(itemsByApp))
	for _, it := range itemsByApp {
		result = append(result, it)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

// bulkEnrichStoreItems queries IStoreBrowseService/GetItems/v1 in batches of 100 AppIDs
// for any owned apps still missing titles ("App <id>"), with polite concurrency and retry on HTTP 429.
func (c *Client) bulkEnrichStoreItems(ctx context.Context, itemsByApp map[uint32]models.LibraryItem) {
	if len(itemsByApp) == 0 {
		return
	}

	var appIDs []uint32
	for id, it := range itemsByApp {
		if it.Name == "" || strings.HasPrefix(it.Name, "App ") {
			appIDs = append(appIDs, id)
		}
	}
	if len(appIDs) == 0 {
		return
	}
	sort.Slice(appIDs, func(i, j int) bool { return appIDs[i] < appIDs[j] })

	const batchSize = 100
	var batches [][]uint32
	for i := 0; i < len(appIDs); i += batchSize {
		end := i + batchSize
		if end > len(appIDs) {
			end = len(appIDs)
		}
		batches = append(batches, appIDs[i:end])
	}

	type storeBrowseItem struct {
		AppID     uint32 `json:"appid"`
		Name      string `json:"name"`
		IsFree    bool   `json:"is_free"`
		BasicInfo struct {
			Developers []struct {
				Name string `json:"name"`
			} `json:"developers"`
			Publishers []struct {
				Name string `json:"name"`
			} `json:"publishers"`
		} `json:"basic_info"`
	}

	var mu sync.Mutex
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup

	for _, batch := range batches {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(ids []uint32) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			type idObj struct {
				AppID uint32 `json:"appid"`
			}
			reqIDs := make([]idObj, len(ids))
			for i, id := range ids {
				reqIDs[i] = idObj{AppID: id}
			}
			payload := map[string]any{
				"ids": reqIDs,
				"context": map[string]any{
					"language":     "english",
					"country_code": "US",
				},
				"data_request": map[string]any{
					"include_basic_info": true,
				},
			}
			jb, err := json.Marshal(payload)
			if err != nil {
				return
			}
			params := url.Values{}
			params.Set("input_json", string(jb))
			params.Set("format", "json")

			var body []byte
			for attempt := 0; attempt < 3; attempt++ {
				body, err = c.GetWebAPI(ctx, "IStoreBrowseService/GetItems/v1/", params)
				if err == nil {
					break
				}
				if !strings.Contains(err.Error(), "429") {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(400*(attempt+1)) * time.Millisecond):
				}
			}
			if err != nil {
				return
			}
			var resp struct {
				Response struct {
					StoreItems []storeBrowseItem `json:"store_items"`
				} `json:"response"`
			}
			if json.Unmarshal(body, &resp) != nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()
			for _, si := range resp.Response.StoreItems {
				if si.AppID == 0 {
					continue
				}
				existing, ok := itemsByApp[si.AppID]
				if !ok {
					continue
				}
				if si.Name != "" && (existing.Name == "" || strings.HasPrefix(existing.Name, "App ")) {
					existing.Name = si.Name
				}
				if si.IsFree {
					existing.IsFree = true
				}
				if len(existing.Developers) == 0 && len(si.BasicInfo.Developers) > 0 {
					for _, d := range si.BasicInfo.Developers {
						if d.Name != "" {
							existing.Developers = append(existing.Developers, d.Name)
						}
					}
				}
				if len(existing.Publishers) == 0 && len(si.BasicInfo.Publishers) > 0 {
					for _, p := range si.BasicInfo.Publishers {
						if p.Name != "" {
							existing.Publishers = append(existing.Publishers, p.Name)
						}
					}
				}
				itemsByApp[si.AppID] = existing
			}
		}(batch)
	}
	wg.Wait()
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
			if migrateCachedOutgoingGifts(cached) {
				_ = SaveLibraryCache(cached)
			}
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

// migrateCachedOutgoingGifts upgrades a previously saved LibraryCache if any "Gift Purchase ..." rows
// in Purchases were classified as "gift" instead of "gift_sent", re-merging affected library items.
func migrateCachedOutgoingGifts(cache *LibraryCache) bool {
	if cache == nil {
		return false
	}
	changed := false
	outgoingTx := make(map[string]bool)
	for i := range cache.Purchases {
		if cache.Purchases[i].IsOutgoingGift() {
			if cache.Purchases[i].TransactionID != "" {
				outgoingTx[cache.Purchases[i].TransactionID] = true
			}
			if !cache.Purchases[i].IsGiftForOther || cache.Purchases[i].AcquisitionType == models.AcquisitionGift {
				cache.Purchases[i].IsGiftForOther = true
				if !cache.Purchases[i].IsRefunded {
					cache.Purchases[i].AcquisitionType = models.AcquisitionGiftSent
				}
				changed = true
			}
		}
	}
	for _, it := range cache.Items {
		if (it.MatchedTransaction != "" && outgoingTx[it.MatchedTransaction]) || it.AcquisitionType == models.AcquisitionGiftSent {
			changed = true
			break
		}
	}
	if changed {
		cache.Items = MergeLibraryWithPurchases(cache.Items, cache.Purchases, cache.Licenses)
	}
	return changed
}

// MergeLibraryWithPurchases correlates owned games with purchase history records and license activations.
func MergeLibraryWithPurchases(
	owned []models.LibraryItem,
	purchases []models.PurchaseRecord,
	licenses []models.LicenseRecord,
) []models.LibraryItem {
	// Track outgoing gift transaction IDs and index personal purchases by normalized item title
	outgoingGiftTx := make(map[string]bool)
	purchaseByNorm := make(map[string]models.PurchaseRecord)
	for i := range purchases {
		p := purchases[i]
		if p.IsOutgoingGift() {
			if p.TransactionID != "" {
				outgoingGiftTx[p.TransactionID] = true
			}
			purchases[i].IsGiftForOther = true
			if !p.IsRefunded {
				purchases[i].AcquisitionType = models.AcquisitionGiftSent
			}
			// Outgoing gift purchases were bought for someone else; do not index them for the user's own library items
			continue
		}
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
		// If this item previously matched an outgoing gift transaction in an older cache, reset its purchase fields
		if (item.MatchedTransaction != "" && outgoingGiftTx[item.MatchedTransaction]) || item.AcquisitionType == models.AcquisitionGiftSent {
			item.PurchaseDate = time.Time{}
			item.AcquisitionType = ""
			item.PriceCents = 0
			item.PriceDisplay = ""
			item.IsRefunded = false
			item.IsBundle = false
			item.MatchedTransaction = ""
			item.AcquisitionReason = ""
		}

		wasStoreFree := item.IsFree
		norm := ""
		if !strings.HasPrefix(item.Name, "App ") {
			norm = NormalizeTitle(item.Name)
		}

		_, hasExactLicense := licenseByNorm[norm]

		// 1. Check exact or high-confidence substring match in personal PurchaseRecords (excluding outgoing gifts)
		if p, ok := findMatchingPurchase(norm, purchaseByNorm, purchases, hasExactLicense); ok {
			item.PurchaseDate = p.Date
			item.AcquisitionType = p.AcquisitionType
			item.PriceCents = p.PriceCents
			item.PriceDisplay = p.PriceDisplay
			item.IsFree = p.IsFree || wasStoreFree
			item.IsRefunded = p.IsRefunded
			item.IsBundle = p.IsBundle
			item.MatchedTransaction = p.TransactionID
			item.AcquisitionReason = fmt.Sprintf(
				"Matched Steam Purchase History Transaction #%s (%s): Type=%q -> %q, Price=%s, Items=[%s]",
				p.TransactionID,
				p.Date.Format("2006-01-02"),
				p.TypeDescription,
				p.AcquisitionType,
				p.PriceDisplay,
				strings.Join(p.Items, "; "),
			)
		}

		// 2. Supplement with LicenseRecords (especially for Retail Keys, Complimentary/Free, Gifts received)
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
				item.AcquisitionReason = fmt.Sprintf(
					"Matched Steam Account License (%s): Method=%q -> %q, Package=%q",
					lic.Date.Format("2006-01-02"),
					lic.AcquisitionMethod,
					lic.AcquisitionType,
					lic.ItemName,
				)
			}
		}

		// 3. If store metadata indicates the item is free and no paid acquisition was matched, mark as free
		if item.IsFree && (item.AcquisitionType == "" || item.AcquisitionType == models.AcquisitionUnknown) {
			item.AcquisitionType = models.AcquisitionFree
			if item.PriceDisplay == "" {
				item.PriceDisplay = "Free"
			}
			if item.AcquisitionReason == "" {
				item.AcquisitionReason = "Steam Store / AppInfo metadata indicates item is free (is_free=true)"
			}
		}

		if item.AcquisitionType == "" {
			item.AcquisitionType = models.AcquisitionUnknown
		}
		result[i] = item
	}

	return result
}

// ExplainItemAcquisition finds all purchase history transactions and license activations matching an item
// and produces a detailed human-readable explanation of why the item has its current AcquisitionType.
func ExplainItemAcquisition(
	item models.LibraryItem,
	purchases []models.PurchaseRecord,
	licenses []models.LicenseRecord,
) (matchedPurchases []models.PurchaseRecord, matchedLicenses []models.LicenseRecord, explanation string) {
	norm := ""
	if !strings.HasPrefix(item.Name, "App ") {
		norm = NormalizeTitle(item.Name)
	}

	hasExactLicense := false
	for _, lic := range licenses {
		cand := NormalizeTitle(lic.ItemName)
		if cand != "" && cand == norm {
			hasExactLicense = true
			matchedLicenses = append(matchedLicenses, lic)
		}
	}
	if len(matchedLicenses) == 0 && norm != "" {
		for _, lic := range licenses {
			cand := NormalizeTitle(lic.ItemName)
			if isFuzzyTitleMatch(norm, cand) {
				matchedLicenses = append(matchedLicenses, lic)
			}
		}
	}

	ignoredOutgoingGifts := 0
	for _, p := range purchases {
		matched := false
		if item.MatchedTransaction != "" && p.TransactionID == item.MatchedTransaction {
			matched = true
		}
		if !matched && norm != "" {
			for _, rawTitle := range p.Items {
				cand := NormalizeTitle(rawTitle)
				if cand == norm || (!hasExactLicense && isFuzzyTitleMatch(norm, cand)) {
					matched = true
					break
				}
			}
		}
		if matched {
			if p.IsOutgoingGift() {
				p.IsGiftForOther = true
				if !p.IsRefunded {
					p.AcquisitionType = models.AcquisitionGiftSent
				}
				ignoredOutgoingGifts++
			}
			matchedPurchases = append(matchedPurchases, p)
		}
	}

	if item.AcquisitionReason != "" {
		explanation = item.AcquisitionReason
	} else if len(matchedPurchases) > 0 {
		var chosen *models.PurchaseRecord
		for i := range matchedPurchases {
			mp := &matchedPurchases[i]
			if item.MatchedTransaction != "" && mp.TransactionID == item.MatchedTransaction {
				chosen = mp
				break
			}
			if chosen == nil && !mp.IsOutgoingGift() {
				chosen = mp
			}
		}
		if chosen != nil {
			explanation = fmt.Sprintf(
				"Matched Steam Purchase History Transaction #%s on %s: Steam Type=%q (classified as %q), Price=%s",
				chosen.TransactionID,
				chosen.Date.Format("2006-01-02"),
				chosen.TypeDescription,
				chosen.AcquisitionType,
				chosen.PriceDisplay,
			)
		} else {
			explanation = "Only outgoing 'Gift Purchase' transactions (purchased for other users) were found in purchase history"
		}
	} else if len(matchedLicenses) > 0 {
		lic := matchedLicenses[0]
		explanation = fmt.Sprintf(
			"Matched Steam Account License on %s: Acquisition Method=%q (classified as %q), Package=%q",
			lic.Date.Format("2006-01-02"),
			lic.AcquisitionMethod,
			lic.AcquisitionType,
			lic.ItemName,
		)
	} else if item.IsFree || item.AcquisitionType == models.AcquisitionFree {
		explanation = "Classified as Free via Steam Store / local Steam appinfo.vdf & packageinfo.vdf metadata"
	} else {
		explanation = "No matching purchase transaction or license entry found by title in cached history"
	}

	if ignoredOutgoingGifts > 0 {
		explanation += fmt.Sprintf(
			" [Ignored %d outgoing 'Gift Purchase' transaction(s) bought for other users]",
			ignoredOutgoingGifts,
		)
	}

	return matchedPurchases, matchedLicenses, explanation
}

func findMatchingPurchase(norm string, exactMap map[string]models.PurchaseRecord, all []models.PurchaseRecord, hasExactLicense bool) (models.PurchaseRecord, bool) {
	if norm == "" {
		return models.PurchaseRecord{}, false
	}
	if p, ok := exactMap[norm]; ok {
		return p, true
	}
	if hasExactLicense {
		return models.PurchaseRecord{}, false
	}
	for _, p := range all {
		if p.IsOutgoingGift() {
			continue
		}
		for _, rawTitle := range p.Items {
			cand := NormalizeTitle(rawTitle)
			if isFuzzyTitleMatch(norm, cand) {
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
		if isFuzzyTitleMatch(norm, cand) {
			return lic, true
		}
	}
	return models.LicenseRecord{}, false
}

// isFuzzyTitleMatch checks whether two normalized titles match on word boundaries with sufficient length and overlap,
// avoiding false positives from short titles like "N++" ("n") or "Oik" ("oik").
func isFuzzyTitleMatch(a, b string) bool {
	if len(a) < 6 || len(b) < 6 {
		return false
	}
	shorter, longer := a, b
	if len(shorter) > len(longer) {
		shorter, longer = b, a
	}
	if len(shorter)*10 < len(longer)*6 {
		return false
	}
	return strings.Contains(" "+longer+" ", " "+shorter+" ")
}

