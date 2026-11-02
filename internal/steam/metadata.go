package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/models"
)

// MetadataStore manages fetching and disk-caching of Steam Store metadata for AppIDs.
type MetadataStore struct {
	mu    sync.RWMutex
	Items map[uint32]models.StoreMetadata `json:"items"`
	dirty bool
}

func metadataCachePath() (string, error) {
	dir, err := auth.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache", "metadata_cache.json"), nil
}

// LoadMetadataStore loads cached store metadata from disk.
func LoadMetadataStore() *MetadataStore {
	ms := &MetadataStore{
		Items: make(map[uint32]models.StoreMetadata),
	}
	path, err := metadataCachePath()
	if err != nil {
		return ms
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ms
	}
	_ = json.Unmarshal(data, ms)
	if ms.Items == nil {
		ms.Items = make(map[uint32]models.StoreMetadata)
	}
	return ms
}

// Save writes the metadata store to disk if modified.
func (ms *MetadataStore) Save() error {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if !ms.dirty {
		return nil
	}
	path, err := metadataCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ms, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// Get returns cached metadata for an AppID if available.
func (ms *MetadataStore) Get(appID uint32) (models.StoreMetadata, bool) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	m, ok := ms.Items[appID]
	return m, ok
}

// Put stores metadata for an AppID in memory and marks the store dirty.
func (ms *MetadataStore) Put(meta models.StoreMetadata) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.Items[meta.AppID] = meta
	ms.dirty = true
}

// EnrichItems populates Genres, Tags, Categories, Developers, Publishers, and DeckCompatibility on LibraryItems.
// If fetchMissing is true, uncached apps are fetched from the Steam Store API up to maxFetch items.
func (c *Client) EnrichItems(ctx context.Context, items []models.LibraryItem, fetchMissing bool, maxFetch int, logf func(string)) []models.LibraryItem {
	ms := LoadMetadataStore()
	defer func() {
		_ = ms.Save()
	}()

	if maxFetch <= 0 {
		maxFetch = 150
	}
	fetchedCount := 0

	out := make([]models.LibraryItem, len(items))
	for i, item := range items {
		meta, ok := ms.Get(item.AppID)
		if !ok && fetchMissing && fetchedCount < maxFetch {
			if logf != nil {
				logf(fmt.Sprintf("Fetching store metadata for %s (AppID %d) [%d/%d]...", item.Name, item.AppID, fetchedCount+1, maxFetch))
			}
			fetchedMeta, err := c.FetchAppMetadata(ctx, item.AppID)
			if err == nil {
				ms.Put(fetchedMeta)
				meta = fetchedMeta
				ok = true
			}
			fetchedCount++
			// Polite delay to avoid Steam Store API rate limits
			select {
			case <-ctx.Done():
				fetchMissing = false
			case <-time.After(220 * time.Millisecond):
			}
		}

		if ok {
			if item.Name == "" || strings.HasPrefix(item.Name, "App ") {
				if meta.Name != "" {
					item.Name = meta.Name
				}
			}
			item.Genres = meta.Genres
			item.Tags = meta.Tags
			item.Categories = meta.Categories
			item.Developers = meta.Developers
			item.Publishers = meta.Publishers
			if meta.DeckCompatibility != "" {
				item.DeckCompatibility = meta.DeckCompatibility
			}
			if meta.IsFree && item.AcquisitionType == models.AcquisitionUnknown {
				item.IsFree = true
				item.AcquisitionType = models.AcquisitionFree
			}
		}
		if item.DeckCompatibility == "" {
			item.DeckCompatibility = models.DeckUnknown
		}
		out[i] = item
	}
	return out
}

var storeTagRegex = regexp.MustCompile(`class="app_tag"[^>]*>\s*([^<\r\n]+?)\s*</(?:a|div)>`)

// FetchAppMetadata queries Steam Store appdetails, Deck compatibility, and user tags for a single AppID.
func (c *Client) FetchAppMetadata(ctx context.Context, appID uint32) (models.StoreMetadata, error) {
	meta := models.StoreMetadata{
		AppID:             appID,
		DeckCompatibility: models.DeckUnknown,
		FetchedAt:         time.Now(),
	}

	// 1. Query /api/appdetails
	appIDStr := strconv.FormatUint(uint64(appID), 10)
	detailsBody, err := c.GetStore(ctx, fmt.Sprintf("/api/appdetails?appids=%s&l=english", appIDStr))
	if err != nil {
		return meta, err
	}

	var rawMap map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name       string   `json:"name"`
			Type       string   `json:"type"`
			IsFree     bool     `json:"is_free"`
			Developers []string `json:"developers"`
			Publishers []string `json:"publishers"`
			Genres     []struct {
				ID          string `json:"id"`
				Description string `json:"description"`
			} `json:"genres"`
			Categories []struct {
				ID          int    `json:"id"`
				Description string `json:"description"`
			} `json:"categories"`
			PriceOverview *struct {
				Final int `json:"final"`
			} `json:"price_overview"`
		} `json:"data"`
	}

	if err := json.Unmarshal(detailsBody, &rawMap); err == nil {
		if entry, ok := rawMap[appIDStr]; ok && entry.Success {
			d := entry.Data
			meta.Name = d.Name
			meta.Type = d.Type
			meta.IsFree = d.IsFree
			meta.Developers = d.Developers
			meta.Publishers = d.Publishers
			for _, g := range d.Genres {
				if g.Description != "" {
					meta.Genres = append(meta.Genres, g.Description)
				}
			}
			for _, cat := range d.Categories {
				if cat.Description != "" {
					meta.Categories = append(meta.Categories, cat.Description)
				}
			}
			if d.PriceOverview != nil {
				meta.PriceCents = d.PriceOverview.Final
			}
		}
	}

	// 2. Query Steam Deck compatibility report
	deckBody, err := c.GetStore(ctx, fmt.Sprintf("/saleaction/ajaxgetdeckappcompatibilityreport?nAppID=%s", appIDStr))
	if err == nil {
		var deckResp struct {
			Success int `json:"success"`
			Results struct {
				ResolvedCategory int `json:"resolved_category"`
			} `json:"results"`
		}
		if json.Unmarshal(deckBody, &deckResp) == nil && deckResp.Success == 1 {
			switch deckResp.Results.ResolvedCategory {
			case 3:
				meta.DeckCompatibility = models.DeckVerified
			case 2:
				meta.DeckCompatibility = models.DeckPlayable
			case 1:
				meta.DeckCompatibility = models.DeckUnsupported
			default:
				meta.DeckCompatibility = models.DeckUnknown
			}
		}
	}

	// 3. Extract popular user-defined tags from store page or hover snippet
	hoverBody, err := c.GetStore(ctx, fmt.Sprintf("/apphoverpublic/%s?l=english", appIDStr))
	if err == nil {
		matches := storeTagRegex.FindAllStringSubmatch(string(hoverBody), -1)
		for _, m := range matches {
			tag := strings.TrimSpace(html.UnescapeString(m[1]))
			if tag != "" && tag != "+" {
				meta.Tags = append(meta.Tags, tag)
			}
		}
	}

	return meta, nil
}

