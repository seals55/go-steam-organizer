package steam

import (
	"testing"

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

