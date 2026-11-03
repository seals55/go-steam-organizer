package steam

import (
	"strings"
	"testing"
	"time"

	"github.com/csull/go-steam-organizer/internal/models"
)

const sampleHistoryHTML = `
<table class="wallet_history_table">
  <tbody>
    <tr class="wallet_table_row" onclick="window.location='https://help.steampowered.com/en/wizard/HelpWithTransaction?transid=111222333'">
      <td class="wht_date">Nov 24, 2025</td>
      <td class="wht_items">
        <div>Hades II</div>
      </td>
      <td class="wht_type">
        <div>Purchase</div>
      </td>
      <td class="wht_total">$29.99</td>
    </tr>
    <tr class="wallet_table_row" onclick="window.location='https://help.steampowered.com/en/wizard/HelpWithTransaction?transid=444555666'">
      <td class="wht_date">Dec 20, 2024</td>
      <td class="wht_items">
        <div>Valve Complete Pack</div>
        <div>Portal</div>
        <div>Portal 2</div>
      </td>
      <td class="wht_type">
        <div>Purchase</div>
      </td>
      <td class="wht_total">$2.98</td>
    </tr>
    <tr class="wallet_table_row">
      <td class="wht_date">Jan 10, 2025</td>
      <td class="wht_items">
        <div>Bad Launch Game</div>
      </td>
      <td class="wht_type">
        <div>Refund</div>
      </td>
      <td class="wht_total wht_refunded">$19.99</td>
    </tr>
  </tbody>
</table>
`

const sampleLicensesHTML = `
<table class="account_table">
  <tr>
    <td class="license_date_col">Oct 12, 2023</td>
    <td>
      <div class="free_license_remove_link"><a href="#">Remove</a></div>
       Baldur's Gate 3 - Retail Key
    </td>
    <td class="license_acquisition_col">Retail</td>
  </tr>
  <tr>
    <td class="license_date_col">May 5, 2022</td>
    <td>
      Apex Legends
    </td>
    <td class="license_acquisition_col">Complimentary</td>
  </tr>
</table>
`

func TestParsePurchaseHistoryAndMerge(t *testing.T) {
	purchases := ParsePurchaseHistoryHTML(sampleHistoryHTML)
	if len(purchases) != 3 {
		t.Fatalf("expected 3 purchase rows, got %d", len(purchases))
	}

	if purchases[0].PriceCents != 2999 || purchases[0].AcquisitionType != models.AcquisitionPurchase || purchases[0].Date.Year() != 2025 {
		t.Errorf("unexpected first purchase: %+v", purchases[0])
	}
	if !purchases[1].IsBundle || purchases[1].AcquisitionType != models.AcquisitionBundle || purchases[1].PriceCents != 298 {
		t.Errorf("expected second purchase to be bundle ($2.98), got: %+v", purchases[1])
	}
	if !purchases[2].IsRefunded || purchases[2].AcquisitionType != models.AcquisitionRefunded {
		t.Errorf("expected third purchase to be refunded, got: %+v", purchases[2])
	}

	licenses := ParseAccountLicensesHTML(sampleLicensesHTML)
	if len(licenses) != 2 {
		t.Fatalf("expected 2 license rows, got %d", len(licenses))
	}
	if licenses[0].AcquisitionType != models.AcquisitionRetailKey {
		t.Errorf("expected Retail Key acquisition type, got %s", licenses[0].AcquisitionType)
	}
	if licenses[1].AcquisitionType != models.AcquisitionFree {
		t.Errorf("expected Free acquisition type, got %s", licenses[1].AcquisitionType)
	}

	owned := []models.LibraryItem{
		{AppID: 1145350, Name: "Hades II", PlaytimeMinutes: 240},
		{AppID: 620, Name: "Portal 2", PlaytimeMinutes: 600},
		{AppID: 1086940, Name: "Baldur's Gate 3", PlaytimeMinutes: 3000},
		{AppID: 1172470, Name: "Apex Legends", PlaytimeMinutes: 0},
	}

	merged := MergeLibraryWithPurchases(owned, purchases, licenses)
	if merged[0].PurchaseYear() != 2025 || merged[0].PriceCents != 2999 {
		t.Errorf("Hades II merge mismatch: %+v", merged[0])
	}
	if !merged[1].IsBundle || merged[1].AcquisitionType != models.AcquisitionBundle {
		t.Errorf("Portal 2 bundle merge mismatch: %+v", merged[1])
	}
	if merged[2].AcquisitionType != models.AcquisitionRetailKey || merged[2].PurchaseYear() != 2023 {
		t.Errorf("Baldur's Gate 3 license merge mismatch: %+v", merged[2])
	}
	if !merged[3].IsFree || merged[3].AcquisitionType != models.AcquisitionFree {
		t.Errorf("Apex Legends free license merge mismatch: %+v", merged[3])
	}
}

func TestShortPurchaseTitleDoesNotFalsePositiveMatchFreeGames(t *testing.T) {
	purchases := []models.PurchaseRecord{
		{
			TransactionID:   "347173482651410021",
			Items:           []string{"Siralim 2", "N++", "Caveblazers"},
			AcquisitionType: models.AcquisitionBundle,
			PriceCents:      3831,
			PriceDisplay:    "$38.31",
			IsFree:          false,
			IsBundle:        true,
		},
	}
	licenses := []models.LicenseRecord{
		{
			ItemName:          "ChallaChat",
			AcquisitionMethod: "Complimentary",
			AcquisitionType:   models.AcquisitionFree,
		},
	}
	owned := []models.LibraryItem{
		{AppID: 4480260, Name: "ChallaChat", IsFree: true},
		{AppID: 1021770, Name: "Wanba Warriors", IsFree: false},
		{AppID: 230270, Name: "N++", IsFree: false},
	}

	merged := MergeLibraryWithPurchases(owned, purchases, licenses)
	// ChallaChat must match its Complimentary license / store free flag, NOT "N++"
	if !merged[0].IsFree || merged[0].AcquisitionType != models.AcquisitionFree || merged[0].MatchedTransaction != "" {
		t.Errorf("expected ChallaChat to be free/complimentary, got: %+v", merged[0])
	}
	// Wanba Warriors (contains 'n') must NOT match "N++" ("n")
	if merged[1].MatchedTransaction == "347173482651410021" {
		t.Errorf("expected Wanba Warriors NOT to match N++ bundle transaction, got: %+v", merged[1])
	}
	// N++ itself MUST match the bundle transaction via exact match
	if merged[2].MatchedTransaction != "347173482651410021" || !merged[2].IsBundle {
		t.Errorf("expected N++ to match bundle transaction, got: %+v", merged[2])
	}
}

func TestNonGameTransactionFilteringAndExactLicensePriority(t *testing.T) {
	marketHTML := `
	<table class="wallet_history_table">
	  <tr class="wallet_table_row">
	    <td class="wht_date">Nov 1, 2025</td>
	    <td class="wht_items"><div>Steam Community Market</div></td>
	    <td class="wht_type"><div>Market Transaction</div></td>
	    <td class="wht_total">$0.05</td>
	  </tr>
	  <tr class="wallet_table_row">
	    <td class="wht_date">Nov 1, 2025</td>
	    <td class="wht_items"><div>Purchased $10.00 Wallet Credit</div></td>
	    <td class="wht_type"><div>Purchase</div></td>
	    <td class="wht_total">$10.00</td>
	  </tr>
	</table>`
	if recs := ParsePurchaseHistoryHTML(marketHTML); len(recs) != 0 {
		t.Errorf("expected non-game market/wallet rows to be filtered out, got %d", len(recs))
	}
}

func TestOutgoingGiftPurchasesIgnoredInFavorOfPersonalPurchase(t *testing.T) {
	giftAndPersonalHTML := `
	<table class="wallet_history_table">
	  <tr class="wallet_table_row" onclick="window.location='https://help.steampowered.com/en/wizard/HelpWithTransaction?transid=347173235088937216'">
	    <td class="wht_date">Dec 22, 2016</td>
	    <td class="wht_items"><div>Aliens versus Predator Classic 2000</div></td>
	    <td class="wht_type"><div>Gift Purchase</div><div class="wht_type_sub">Wallet</div></td>
	    <td class="wht_total">$1.24</td>
	  </tr>
	  <tr class="wallet_table_row" onclick="window.location='https://help.steampowered.com/en/wizard/HelpWithTransaction?transid=347173235088933464'">
	    <td class="wht_date">Dec 22, 2016</td>
	    <td class="wht_items"><div>Aliens versus Predator Classic 2000</div></td>
	    <td class="wht_type"><div>Gift Purchase</div><div class="wht_type_sub">Wallet</div></td>
	    <td class="wht_total">$1.24</td>
	  </tr>
	  <tr class="wallet_table_row" onclick="window.location='https://help.steampowered.com/en/wizard/HelpWithTransaction?transid=347173235088927766'">
	    <td class="wht_date">Dec 22, 2016</td>
	    <td class="wht_items"><div>Aliens versus Predator Classic 2000</div></td>
	    <td class="wht_type"><div>Purchase</div><div class="wht_type_sub">Wallet</div></td>
	    <td class="wht_total">$1.24</td>
	  </tr>
	</table>`

	purchases := ParsePurchaseHistoryHTML(giftAndPersonalHTML)
	if len(purchases) != 3 {
		t.Fatalf("expected 3 purchase rows, got %d", len(purchases))
	}
	if !purchases[0].IsOutgoingGift() || purchases[0].AcquisitionType != models.AcquisitionGiftSent {
		t.Errorf("expected row 0 ('Gift Purchase Wallet') to be AcquisitionGiftSent, got %+v", purchases[0])
	}
	if purchases[2].IsOutgoingGift() || purchases[2].AcquisitionType != models.AcquisitionPurchase {
		t.Errorf("expected row 2 ('Purchase Wallet') to be AcquisitionPurchase, got %+v", purchases[2])
	}

	owned := []models.LibraryItem{
		{AppID: 3730, Name: "Aliens versus Predator Classic 2000"},
	}
	merged := MergeLibraryWithPurchases(owned, purchases, nil)
	if merged[0].AcquisitionType != models.AcquisitionPurchase {
		t.Errorf("expected Aliens versus Predator Classic 2000 to be classified as 'purchase', got %q", merged[0].AcquisitionType)
	}
	if merged[0].MatchedTransaction != "347173235088927766" {
		t.Errorf("expected MatchedTransaction '347173235088927766', got %q", merged[0].MatchedTransaction)
	}

	// Also verify migrateCachedOutgoingGifts repairs an older cache where the item was marked 'gift'
	staleCache := &LibraryCache{
		Items: []models.LibraryItem{
			{
				AppID:              3730,
				Name:               "Aliens versus Predator Classic 2000",
				AcquisitionType:    models.AcquisitionGift,
				MatchedTransaction: "347173235088937216",
			},
		},
		Purchases: []models.PurchaseRecord{
			{
				TransactionID:   "347173235088937216",
				Items:           []string{"Aliens versus Predator Classic 2000"},
				TypeDescription: "Gift Purchase Wallet",
				AcquisitionType: models.AcquisitionGift,
				PriceCents:      124,
				PriceDisplay:    "$1.24",
			},
			{
				TransactionID:   "347173235088927766",
				Items:           []string{"Aliens versus Predator Classic 2000"},
				TypeDescription: "Purchase Wallet",
				AcquisitionType: models.AcquisitionPurchase,
				PriceCents:      124,
				PriceDisplay:    "$1.24",
			},
		},
	}
	if !migrateCachedOutgoingGifts(staleCache) {
		t.Errorf("expected migrateCachedOutgoingGifts to detect and migrate stale outgoing gift cache")
	}
	if staleCache.Items[0].AcquisitionType != models.AcquisitionPurchase || staleCache.Items[0].MatchedTransaction != "347173235088927766" {
		t.Errorf("expected migrated item to be purchase (#347173235088927766), got %+v", staleCache.Items[0])
	}
}

func TestExplainItemAcquisitionAndHelpers(t *testing.T) {
	purchases := []models.PurchaseRecord{
		{
			TransactionID:   "tx-gift-out",
			Date:            time.Date(2016, 12, 22, 0, 0, 0, 0, time.UTC),
			Items:           []string{"Aliens versus Predator Classic 2000", "Only Gifted Game"},
			TypeDescription: "Gift Purchase Wallet",
			AcquisitionType: models.AcquisitionGiftSent,
			IsGiftForOther:  true,
			PriceCents:      124,
			PriceDisplay:    "$1.24",
		},
		{
			TransactionID:   "tx-personal",
			Date:            time.Date(2016, 12, 22, 0, 0, 0, 0, time.UTC),
			Items:           []string{"Aliens versus Predator Classic 2000"},
			TypeDescription: "Purchase Wallet",
			AcquisitionType: models.AcquisitionPurchase,
			PriceCents:      124,
			PriceDisplay:    "$1.24",
		},
	}
	licenses := []models.LicenseRecord{
		{
			Date:              time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC),
			ItemName:          "Gifted From Friend Complete Edition",
			AcquisitionMethod: "Gift/Guest Pass",
			AcquisitionType:   models.AcquisitionGift,
		},
	}

	// 1. Item with MatchedTransaction & outgoing gift ignored
	avp := models.LibraryItem{
		AppID:              3730,
		Name:               "Aliens versus Predator Classic 2000",
		AcquisitionType:    models.AcquisitionPurchase,
		MatchedTransaction: "tx-personal",
	}
	mp, ml, expl := ExplainItemAcquisition(avp, purchases, licenses)
	if len(mp) != 2 || len(ml) != 0 || !strings.Contains(expl, "tx-personal") || !strings.Contains(expl, "Ignored 1 outgoing") {
		t.Errorf("unexpected explanation for avp: mp=%d ml=%d expl=%q", len(mp), len(ml), expl)
	}

	// 2. Item with explicit AcquisitionReason
	avp.AcquisitionReason = "Explicitly set reason"
	_, _, explExplicit := ExplainItemAcquisition(avp, purchases, licenses)
	if !strings.Contains(explExplicit, "Explicitly set reason") || !strings.Contains(explExplicit, "Ignored 1 outgoing") {
		t.Errorf("expected explicit reason + ignored note, got %q", explExplicit)
	}

	// 3. Item where only outgoing gift purchases matched in history
	onlyGifted := models.LibraryItem{
		AppID: 9991,
		Name:  "Only Gifted Game",
	}
	_, _, explOnlyGift := ExplainItemAcquisition(onlyGifted, purchases, nil)
	if !strings.Contains(explOnlyGift, "Only outgoing 'Gift Purchase' transactions") {
		t.Errorf("expected only-outgoing-gift message, got %q", explOnlyGift)
	}

	// 4. Item matched via fuzzy license match
	giftedFriend := models.LibraryItem{
		AppID:           9992,
		Name:            "Gifted From Friend",
		AcquisitionType: models.AcquisitionGift,
	}
	_, mlFriend, explFriend := ExplainItemAcquisition(giftedFriend, nil, licenses)
	if len(mlFriend) != 1 || !strings.Contains(explFriend, "Gift/Guest Pass") {
		t.Errorf("expected fuzzy license match for Gifted From Friend, got ml=%d expl=%q", len(mlFriend), explFriend)
	}

	// 5. Free item fallback & unknown item fallback
	freeItem := models.LibraryItem{AppID: 9993, Name: "Some Free Game", IsFree: true, AcquisitionType: models.AcquisitionFree}
	if _, _, explFree := ExplainItemAcquisition(freeItem, nil, nil); !strings.Contains(explFree, "Classified as Free") {
		t.Errorf("expected Free explanation, got %q", explFree)
	}
	unkItem := models.LibraryItem{AppID: 9994, Name: "Unknown Game", AcquisitionType: models.AcquisitionUnknown}
	if _, _, explUnk := ExplainItemAcquisition(unkItem, nil, nil); !strings.Contains(explUnk, "No matching purchase") {
		t.Errorf("expected Unknown explanation, got %q", explUnk)
	}

	// 6. Test classifyLicenseMethod across all branches
	if got := classifyLicenseMethod("Gift/Guest Pass", "Portal"); got != models.AcquisitionGift {
		t.Errorf("expected AcquisitionGift, got %s", got)
	}
	if got := classifyLicenseMethod("Retail", "Humble Indie Bundle 5"); got != models.AcquisitionBundle {
		t.Errorf("expected AcquisitionBundle for retail bundle, got %s", got)
	}
	if got := classifyLicenseMethod("Steam Store", "Valve Complete Bundle"); got != models.AcquisitionBundle {
		t.Errorf("expected AcquisitionBundle for store bundle, got %s", got)
	}
	if got := classifyLicenseMethod("Steam Store", "Half-Life 2"); got != models.AcquisitionPurchase {
		t.Errorf("expected AcquisitionPurchase for store single item, got %s", got)
	}
	if got := classifyLicenseMethod("Hardware Promo", "AMD Game"); got != models.AcquisitionUnknown {
		t.Errorf("expected AcquisitionUnknown, got %s", got)
	}

	// 7. Test parseSteamDate and parsePriceCents formats
	for _, ds := range []string{"24 Nov, 2025", "2025-11-24", "Oct 15", "15 Oct"} {
		if parseSteamDate(ds).IsZero() {
			t.Errorf("expected non-zero parsed date for %q", ds)
		}
	}
	if !parseSteamDate("").IsZero() || !parseSteamDate("not-a-date").IsZero() {
		t.Errorf("expected zero time for invalid dates")
	}
	if parsePriceCents("Free") != 0 || parsePriceCents("--") != 0 || parsePriceCents("no digits") != 0 {
		t.Errorf("expected 0 cents for non-paid strings")
	}
}

func TestAmpersandTitleNormalizationAndMultiItemCartMigration(t *testing.T) {
	if got, want := NormalizeTitle("Sorcery! Parts 1 & 2"), NormalizeTitle("Sorcery! Parts 1 and 2"); got != want {
		t.Fatalf("expected NormalizeTitle('&') == NormalizeTitle('and'), got %q vs %q", got, want)
	}

	cartHTML := `
	<table class="wallet_history_table">
	  <tr class="wallet_table_row" onclick="window.location='https://help.steampowered.com/en/wizard/HelpWithTransaction?transid=389157488265053604'">
	    <td class="wht_date">Nov 2, 2026</td>
	    <td class="wht_items">
	      <div>Sorcery! Parts 1 and 2</div>
	      <div>Sorcery! Part 3</div>
	      <div>Sorcery! Part 4</div>
	      <div>Brotato: Primal Dread</div>
	    </td>
	    <td class="wht_type"><div>Purchase</div><div class="wht_type_sub">Visa **69</div></td>
	    <td class="wht_total">$26.31</td>
	  </tr>
	</table>`

	purchases := ParsePurchaseHistoryHTML(cartHTML)
	if len(purchases) != 1 {
		t.Fatalf("expected 1 purchase row, got %d", len(purchases))
	}
	if purchases[0].IsBundle || purchases[0].AcquisitionType != models.AcquisitionPurchase {
		t.Errorf("expected multi-item cart without bundle keywords to be classified as purchase, got %+v", purchases[0])
	}

	// Simulate an older cache where the multi-item cart was marked as a bundle and "Sorcery! Parts 1 & 2" failed to match
	staleCache := &LibraryCache{
		Items: []models.LibraryItem{
			{
				AppID:             411000,
				Name:              "Sorcery! Parts 1 & 2",
				AcquisitionType:   models.AcquisitionPurchase,
				AcquisitionReason: "Local Steam packageinfo.vdf (BillingType=1 Paid Steam Store Package)",
			},
			{
				AppID:              445040,
				Name:               "Sorcery! Part 3",
				PurchaseDate:       time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
				AcquisitionType:    models.AcquisitionBundle,
				PriceCents:         2631,
				PriceDisplay:       "$26.31",
				IsBundle:           true,
				MatchedTransaction: "389157488265053604",
			},
		},
		Purchases: []models.PurchaseRecord{
			{
				TransactionID:   "389157488265053604",
				Date:            time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
				Items:           []string{"Sorcery! Parts 1 and 2", "Sorcery! Part 3", "Sorcery! Part 4", "Brotato: Primal Dread"},
				TypeDescription: "Purchase Visa **69",
				AcquisitionType: models.AcquisitionBundle,
				PriceCents:      2631,
				PriceDisplay:    "$26.31",
				IsBundle:        true,
			},
		},
	}

	if !migrateCachedOutgoingGifts(staleCache) {
		t.Fatalf("expected migrateCachedOutgoingGifts to detect and migrate multi-item cart and '&' title match")
	}
	for _, it := range staleCache.Items {
		if it.PurchaseYear() != 2026 {
			t.Errorf("expected %q to have PurchaseYear 2026, got %d (PurchaseDate=%v)", it.Name, it.PurchaseYear(), it.PurchaseDate)
		}
		if it.AcquisitionType != models.AcquisitionPurchase || it.IsBundle {
			t.Errorf("expected %q to have AcquisitionType=purchase and IsBundle=false, got type=%q isBundle=%v", it.Name, it.AcquisitionType, it.IsBundle)
		}
		if it.MatchedTransaction != "389157488265053604" || it.PriceCents != 2631 {
			t.Errorf("expected %q to match transaction 389157488265053604 ($26.31), got tx=%q cents=%d", it.Name, it.MatchedTransaction, it.PriceCents)
		}
	}
}



