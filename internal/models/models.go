package models

import (
	"fmt"
	"time"
)

// AcquisitionType represents how a Steam library item or package was acquired.
type AcquisitionType string

const (
	AcquisitionPurchase  AcquisitionType = "purchase"
	AcquisitionFree      AcquisitionType = "free"
	AcquisitionRetailKey AcquisitionType = "retail_key"
	AcquisitionGift      AcquisitionType = "gift"
	AcquisitionBundle    AcquisitionType = "bundle"
	AcquisitionRefunded  AcquisitionType = "refunded"
	AcquisitionUnknown   AcquisitionType = "unknown"
)

// DeckCompatibility represents Steam Deck verification status.
type DeckCompatibility string

const (
	DeckVerified    DeckCompatibility = "verified"
	DeckPlayable    DeckCompatibility = "playable"
	DeckUnsupported DeckCompatibility = "unsupported"
	DeckUnknown     DeckCompatibility = "unknown"
)

// PurchaseRecord represents a single transaction row from Steam's account purchase history.
type PurchaseRecord struct {
	TransactionID   string          `json:"transaction_id,omitempty"`
	Date            time.Time       `json:"date"`
	Items           []string        `json:"items"`
	PackageIDs      []uint32        `json:"package_ids,omitempty"`
	AppIDs          []uint32        `json:"app_ids,omitempty"`
	TypeDescription string          `json:"type_description"`
	AcquisitionType AcquisitionType `json:"acquisition_type"`
	PriceCents      int             `json:"price_cents"`
	PriceDisplay    string          `json:"price_display"`
	IsFree          bool            `json:"is_free"`
	IsRefunded      bool            `json:"is_refunded"`
	IsBundle        bool            `json:"is_bundle"`
}

// LicenseRecord represents an entry from Steam's account licenses / product key activations page.
type LicenseRecord struct {
	Date              time.Time       `json:"date"`
	ItemName          string          `json:"item_name"`
	AcquisitionMethod string          `json:"acquisition_method"`
	AcquisitionType   AcquisitionType `json:"acquisition_type"`
}

// StoreMetadata holds store page details for a Steam AppID.
type StoreMetadata struct {
	AppID             uint32            `json:"app_id"`
	Name              string            `json:"name"`
	Type              string            `json:"type,omitempty"`
	IsFree            bool              `json:"is_free"`
	Genres            []string          `json:"genres,omitempty"`
	Tags              []string          `json:"tags,omitempty"`
	Categories        []string          `json:"categories,omitempty"`
	Developers        []string          `json:"developers,omitempty"`
	Publishers        []string          `json:"publishers,omitempty"`
	ReleaseYear       int               `json:"release_year,omitempty"`
	PriceCents        int               `json:"price_cents,omitempty"`
	DeckCompatibility DeckCompatibility `json:"deck_compatibility,omitempty"`
	FetchedAt         time.Time         `json:"fetched_at"`
}

// LibraryItem combines owned game stats, purchase/license records, and store metadata.
type LibraryItem struct {
	AppID              uint32            `json:"app_id"`
	Name               string            `json:"name"`
	PlaytimeMinutes    int               `json:"playtime_minutes"`
	LastPlayed         time.Time         `json:"last_played,omitempty"`
	PurchaseDate       time.Time         `json:"purchase_date,omitempty"`
	AcquisitionType    AcquisitionType   `json:"acquisition_type"`
	PriceCents         int               `json:"price_cents"`
	PriceDisplay       string            `json:"price_display,omitempty"`
	IsFree             bool              `json:"is_free"`
	IsRefunded         bool              `json:"is_refunded"`
	IsBundle           bool              `json:"is_bundle"`
	Genres             []string          `json:"genres,omitempty"`
	Tags               []string          `json:"tags,omitempty"`
	Categories         []string          `json:"categories,omitempty"`
	Developers         []string          `json:"developers,omitempty"`
	Publishers         []string          `json:"publishers,omitempty"`
	DeckCompatibility  DeckCompatibility `json:"deck_compatibility,omitempty"`
	MatchedTransaction string            `json:"matched_transaction,omitempty"`
}

// PlaytimeHours returns the item's total playtime in hours.
func (item LibraryItem) PlaytimeHours() float64 {
	return float64(item.PlaytimeMinutes) / 60.0
}

// PriceDollars returns the purchase price in USD/currency units.
func (item LibraryItem) PriceDollars() float64 {
	return float64(item.PriceCents) / 100.0
}

// PurchaseYear returns the 4-digit year of purchase, or 0 if unknown.
func (item LibraryItem) PurchaseYear() int {
	if item.PurchaseDate.IsZero() {
		return 0
	}
	return item.PurchaseDate.Year()
}

// SummaryPrice returns a human-readable price string.
func (item LibraryItem) SummaryPrice() string {
	if item.PriceDisplay != "" {
		return item.PriceDisplay
	}
	if item.IsFree || item.PriceCents == 0 {
		return "Free"
	}
	return fmt.Sprintf("$%.2f", item.PriceDollars())
}

// Collection represents a user collection in Steam Library.
type Collection struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Added       []uint32 `json:"added"`
	Removed     []uint32 `json:"removed"`
	IsDynamic   bool     `json:"is_dynamic,omitempty"`
	StorageKey  string   `json:"storage_key,omitempty"`
	Version     string   `json:"version,omitempty"`
	LastUpdated int64    `json:"last_updated,omitempty"`
}

// ContainsApp checks whether the given AppID is in the collection's Added list.
func (c Collection) ContainsApp(appID uint32) bool {
	for _, id := range c.Added {
		if id == appID {
			return true
		}
	}
	return false
}

