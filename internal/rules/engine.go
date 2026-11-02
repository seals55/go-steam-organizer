package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/csull/go-steam-organizer/internal/models"
	"gopkg.in/yaml.v3"
)

// Criteria defines filtering conditions across all 4 supported categories:
// 1. Purchase date / year
// 2. Price / acquisition type
// 3. Store metadata (genres, tags, categories, developer/publisher, Steam Deck compatibility)
// 4. Playtime / played status
type Criteria struct {
	// 1. Purchase Date / Year
	PurchaseYears   []int  `json:"purchase_years,omitempty" yaml:"purchase_years,omitempty"`
	PurchasedAfter  string `json:"purchased_after,omitempty" yaml:"purchased_after,omitempty"`   // YYYY-MM-DD
	PurchasedBefore string `json:"purchased_before,omitempty" yaml:"purchased_before,omitempty"` // YYYY-MM-DD

	// 2. Price / Acquisition Type
	AcquisitionTypes []string `json:"acquisition_types,omitempty" yaml:"acquisition_types,omitempty"` // purchase, free, retail_key, gift, bundle, refunded
	MinPrice         *float64 `json:"min_price,omitempty" yaml:"min_price,omitempty"`                 // Dollars (e.g. 4.99)
	MaxPrice         *float64 `json:"max_price,omitempty" yaml:"max_price,omitempty"`                 // Dollars (e.g. 19.99)
	IsFree           *bool    `json:"is_free,omitempty" yaml:"is_free,omitempty"`
	IsRefunded       *bool    `json:"is_refunded,omitempty" yaml:"is_refunded,omitempty"`
	IsBundle         *bool    `json:"is_bundle,omitempty" yaml:"is_bundle,omitempty"`

	// 3. Store Metadata
	Genres            []string `json:"genres,omitempty" yaml:"genres,omitempty"`
	Tags              []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Categories        []string `json:"categories,omitempty" yaml:"categories,omitempty"`
	Developers        []string `json:"developers,omitempty" yaml:"developers,omitempty"`
	Publishers        []string `json:"publishers,omitempty" yaml:"publishers,omitempty"`
	DeckCompatibility []string `json:"deck_compatibility,omitempty" yaml:"deck_compatibility,omitempty"` // verified, playable, unsupported, unknown

	// 4. Playtime / Played Status
	Unplayed         *bool    `json:"unplayed,omitempty" yaml:"unplayed,omitempty"`
	MinPlaytimeHours *float64 `json:"min_playtime_hours,omitempty" yaml:"min_playtime_hours,omitempty"`
	MaxPlaytimeHours *float64 `json:"max_playtime_hours,omitempty" yaml:"max_playtime_hours,omitempty"`

	// 5. General / Explicit Identifiers
	AppIDs       []uint32 `json:"app_ids,omitempty" yaml:"app_ids,omitempty"`
	NameContains string   `json:"name_contains,omitempty" yaml:"name_contains,omitempty"`
	NameRegex    string   `json:"name_regex,omitempty" yaml:"name_regex,omitempty"`
}

// NeedsStoreMetadata returns true if any store metadata filter is active in this Criteria.
func (c Criteria) NeedsStoreMetadata() bool {
	return len(c.Genres) > 0 ||
		len(c.Tags) > 0 ||
		len(c.Categories) > 0 ||
		len(c.Developers) > 0 ||
		len(c.Publishers) > 0 ||
		len(c.DeckCompatibility) > 0
}

// Rule maps a Criteria filter to a target Steam Collection name (supports templates like "Bought in {{.PurchaseYear}}").
type Rule struct {
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Collection  string   `json:"collection" yaml:"collection"`
	Criteria    Criteria `json:"criteria" yaml:"criteria"`
}

// ConfigFile represents a YAML or JSON configuration containing multiple organization rules.
type ConfigFile struct {
	Rules []Rule `json:"rules" yaml:"rules"`
}

// RuleMatchResult records which items matched a rule and which target collection(s) they resolve to.
type RuleMatchResult struct {
	Rule              Rule
	CollectionMatches map[string][]models.LibraryItem // TargetCollectionName -> matched items
}

// LoadConfigFile parses a YAML or JSON rules file from disk.
func LoadConfigFile(path string) (*ConfigFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read rules file %s: %w", path, err)
	}
	var cfg ConfigFile
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".json" {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid JSON in %s: %w", path, err)
		}
	} else {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid YAML in %s: %w", path, err)
		}
	}
	return &cfg, nil
}

// FilterItems returns all LibraryItems that satisfy the given Criteria.
func FilterItems(items []models.LibraryItem, c Criteria) ([]models.LibraryItem, error) {
	var afterTime, beforeTime time.Time
	var err error
	if c.PurchasedAfter != "" {
		afterTime, err = time.Parse("2006-01-02", strings.TrimSpace(c.PurchasedAfter))
		if err != nil {
			return nil, fmt.Errorf("invalid purchased_after date %q (expected YYYY-MM-DD): %w", c.PurchasedAfter, err)
		}
	}
	if c.PurchasedBefore != "" {
		beforeTime, err = time.Parse("2006-01-02", strings.TrimSpace(c.PurchasedBefore))
		if err != nil {
			return nil, fmt.Errorf("invalid purchased_before date %q (expected YYYY-MM-DD): %w", c.PurchasedBefore, err)
		}
		// Include the entire end day
		beforeTime = beforeTime.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	}

	var nameRe *regexp.Regexp
	if c.NameRegex != "" {
		nameRe, err = regexp.Compile("(?i)" + c.NameRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid name_regex %q: %w", c.NameRegex, err)
		}
	}

	var matched []models.LibraryItem
	for _, item := range items {
		if matchesCriteria(item, c, afterTime, beforeTime, nameRe) {
			matched = append(matched, item)
		}
	}
	return matched, nil
}

// EvaluateRules runs all rules in a ConfigFile against the library items and groups them by target collection.
func EvaluateRules(items []models.LibraryItem, cfg *ConfigFile) ([]RuleMatchResult, error) {
	var results []RuleMatchResult
	for _, rule := range cfg.Rules {
		matchedItems, err := FilterItems(items, rule.Criteria)
		if err != nil {
			return nil, fmt.Errorf("rule %q error: %w", rule.Name, err)
		}
		colMap := make(map[string][]models.LibraryItem)
		for _, item := range matchedItems {
			colName, err := renderCollectionName(rule.Collection, item)
			if err != nil || strings.TrimSpace(colName) == "" {
				continue
			}
			colMap[colName] = append(colMap[colName], item)
		}
		results = append(results, RuleMatchResult{
			Rule:              rule,
			CollectionMatches: colMap,
		})
	}
	return results, nil
}

func renderCollectionName(tmplStr string, item models.LibraryItem) (string, error) {
	if !strings.Contains(tmplStr, "{{") {
		return strings.TrimSpace(tmplStr), nil
	}
	// If template uses PurchaseYear and the item has no known purchase year, skip
	if strings.Contains(tmplStr, "PurchaseYear") && item.PurchaseYear() == 0 {
		return "", nil
	}
	t, err := template.New("col").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	data := struct {
		models.LibraryItem
		PurchaseYear int
	}{
		LibraryItem:  item,
		PurchaseYear: item.PurchaseYear(),
	}
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

var templatePlaceholderRegex = regexp.MustCompile(`\{\{.*?\}\}`)

// RuleMatchesCollectionName checks whether an existing collection name matches a rule's collection specification
// (supporting both exact case-insensitive names like "Purchased" and template patterns like "Bought in {{.PurchaseYear}}").
func RuleMatchesCollectionName(ruleCollectionSpec, colName string) bool {
	spec := strings.TrimSpace(ruleCollectionSpec)
	target := strings.TrimSpace(colName)
	if spec == "" || target == "" {
		return false
	}
	if !strings.Contains(spec, "{{") {
		return strings.EqualFold(spec, target)
	}

	parts := templatePlaceholderRegex.Split(spec, -1)
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = regexp.QuoteMeta(p)
	}
	pattern := "(?i)^" + strings.Join(escaped, ".+") + "$"
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(target)
}

// MatchingCollectionsForRules returns all existing collections whose names match any rule in cfg.
func MatchingCollectionsForRules(cfg *ConfigFile, existing []models.Collection) []models.Collection {
	if cfg == nil || len(cfg.Rules) == 0 || len(existing) == 0 {
		return nil
	}
	var matched []models.Collection
	for _, col := range existing {
		for _, rule := range cfg.Rules {
			if RuleMatchesCollectionName(rule.Collection, col.Name) {
				matched = append(matched, col)
				break
			}
		}
	}
	return matched
}

func matchesCriteria(item models.LibraryItem, c Criteria, afterTime, beforeTime time.Time, nameRe *regexp.Regexp) bool {
	// 1. Explicit AppIDs
	if len(c.AppIDs) > 0 {
		found := false
		for _, id := range c.AppIDs {
			if item.AppID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Name filters
	if c.NameContains != "" {
		if !strings.Contains(strings.ToLower(item.Name), strings.ToLower(c.NameContains)) {
			return false
		}
	}
	if nameRe != nil && !nameRe.MatchString(item.Name) {
		return false
	}

	// 2. Purchase Date / Year
	if len(c.PurchaseYears) > 0 {
		yr := item.PurchaseYear()
		if yr == 0 {
			return false
		}
		yearMatch := false
		for _, targetYr := range c.PurchaseYears {
			if yr == targetYr {
				yearMatch = true
				break
			}
		}
		if !yearMatch {
			return false
		}
	}
	if !afterTime.IsZero() {
		if item.PurchaseDate.IsZero() || item.PurchaseDate.Before(afterTime) {
			return false
		}
	}
	if !beforeTime.IsZero() {
		if item.PurchaseDate.IsZero() || item.PurchaseDate.After(beforeTime) {
			return false
		}
	}

	// 3. Price / Acquisition Type
	if len(c.AcquisitionTypes) > 0 {
		typeMatch := false
		itemAcq := strings.ToLower(string(item.AcquisitionType))
		for _, t := range c.AcquisitionTypes {
			target := strings.ToLower(strings.TrimSpace(t))
			if itemAcq == target ||
				(target == "refunded" && item.IsRefunded) ||
				(target == "bundle" && item.IsBundle) ||
				(target == "free" && item.IsFree) {
				typeMatch = true
				break
			}
		}
		if !typeMatch {
			return false
		}
	}
	if c.IsFree != nil && item.IsFree != *c.IsFree {
		return false
	}
	if c.IsRefunded != nil && item.IsRefunded != *c.IsRefunded {
		return false
	}
	if c.IsBundle != nil && item.IsBundle != *c.IsBundle {
		return false
	}
	if c.MinPrice != nil {
		if item.PriceDollars() < *c.MinPrice {
			return false
		}
	}
	if c.MaxPrice != nil {
		if item.PriceDollars() > *c.MaxPrice {
			return false
		}
	}

	// 4. Store Metadata
	if len(c.Genres) > 0 && !sliceMatchesAny(item.Genres, c.Genres) {
		return false
	}
	if len(c.Tags) > 0 && !sliceMatchesAny(item.Tags, c.Tags) {
		return false
	}
	if len(c.Categories) > 0 && !sliceMatchesAny(item.Categories, c.Categories) {
		return false
	}
	if len(c.Developers) > 0 && !sliceMatchesAny(item.Developers, c.Developers) {
		return false
	}
	if len(c.Publishers) > 0 && !sliceMatchesAny(item.Publishers, c.Publishers) {
		return false
	}
	if len(c.DeckCompatibility) > 0 {
		deckMatch := false
		itemDeck := strings.ToLower(string(item.DeckCompatibility))
		if itemDeck == "" {
			itemDeck = string(models.DeckUnknown)
		}
		for _, d := range c.DeckCompatibility {
			if strings.EqualFold(itemDeck, strings.TrimSpace(d)) {
				deckMatch = true
				break
			}
		}
		if !deckMatch {
			return false
		}
	}

	// 5. Playtime / Played Status
	if c.Unplayed != nil {
		isUnplayed := item.PlaytimeMinutes == 0
		if isUnplayed != *c.Unplayed {
			return false
		}
	}
	if c.MinPlaytimeHours != nil {
		if item.PlaytimeHours() < *c.MinPlaytimeHours {
			return false
		}
	}
	if c.MaxPlaytimeHours != nil {
		if item.PlaytimeHours() > *c.MaxPlaytimeHours {
			return false
		}
	}

	return true
}

func sliceMatchesAny(itemValues []string, queryValues []string) bool {
	if len(itemValues) == 0 {
		return false
	}
	for _, q := range queryValues {
		qLower := strings.ToLower(strings.TrimSpace(q))
		if qLower == "" {
			continue
		}
		for _, iv := range itemValues {
			ivLower := strings.ToLower(iv)
			if ivLower == qLower || strings.Contains(ivLower, qLower) {
				return true
			}
		}
	}
	return false
}

