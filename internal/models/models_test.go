package models

import (
	"testing"
	"time"
)

func TestLibraryItemHelpers(t *testing.T) {
	item := LibraryItem{
		AppID:           440,
		Name:            "Team Fortress 2",
		PlaytimeMinutes: 150,
		PriceCents:      1999,
		PurchaseDate:    time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC),
	}

	if got := item.PlaytimeHours(); got != 2.5 {
		t.Errorf("expected PlaytimeHours 2.5, got %v", got)
	}
	if got := item.PriceDollars(); got != 19.99 {
		t.Errorf("expected PriceDollars 19.99, got %v", got)
	}
	if got := item.PurchaseYear(); got != 2025 {
		t.Errorf("expected PurchaseYear 2025, got %d", got)
	}
	if got := item.SummaryPrice(); got != "$19.99" {
		t.Errorf("expected SummaryPrice $19.99, got %q", got)
	}

	item.PriceDisplay = "CDN$ 24.99"
	if got := item.SummaryPrice(); got != "CDN$ 24.99" {
		t.Errorf("expected explicit PriceDisplay, got %q", got)
	}

	freeItem := LibraryItem{AppID: 570, IsFree: true}
	if got := freeItem.SummaryPrice(); got != "Free" {
		t.Errorf("expected Free summary price, got %q", got)
	}
	if got := freeItem.PurchaseYear(); got != 0 {
		t.Errorf("expected PurchaseYear 0 for zero time, got %d", got)
	}
}

func TestCollectionContainsApp(t *testing.T) {
	col := Collection{
		ID:    "uc-1",
		Name:  "Action",
		Added: []uint32{440, 620, 730},
	}
	if !col.ContainsApp(620) {
		t.Errorf("expected ContainsApp(620) to be true")
	}
	if col.ContainsApp(999) {
		t.Errorf("expected ContainsApp(999) to be false")
	}
}

func TestPurchaseRecordIsOutgoingGift(t *testing.T) {
	p1 := PurchaseRecord{IsGiftForOther: true}
	if !p1.IsOutgoingGift() {
		t.Errorf("expected IsGiftForOther=true to be outgoing gift")
	}
	p2 := PurchaseRecord{AcquisitionType: AcquisitionGiftSent}
	if !p2.IsOutgoingGift() {
		t.Errorf("expected AcquisitionGiftSent to be outgoing gift")
	}
	p3 := PurchaseRecord{TypeDescription: "Gift Purchase Wallet"}
	if !p3.IsOutgoingGift() {
		t.Errorf("expected 'Gift Purchase Wallet' to be outgoing gift")
	}
	p4 := PurchaseRecord{TypeDescription: "Purchase Wallet", AcquisitionType: AcquisitionPurchase}
	if p4.IsOutgoingGift() {
		t.Errorf("expected personal 'Purchase Wallet' not to be outgoing gift")
	}
}


