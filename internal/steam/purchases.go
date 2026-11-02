package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/csull/go-steam-organizer/internal/models"
)

type historyCursor struct {
	WalletTxnID     any   `json:"wallet_txnid"`
	TimestampNewest int64 `json:"timestamp_newest"`
	Balance         any   `json:"balance"`
	Currency        int   `json:"currency"`
}

// FetchPurchaseHistory retrieves the user's purchase history from store.steampowered.com/account/history/.
func (c *Client) FetchPurchaseHistory(ctx context.Context, maxPages int) ([]models.PurchaseRecord, error) {
	if maxPages <= 0 {
		maxPages = 25
	}

	body, err := c.GetStore(ctx, "/account/history/")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch account history: %w", err)
	}

	pageHTML := string(body)
	// Extract sessionid from page if not already on session
	if c.session != nil && c.session.SessionID == "" {
		if m := sessionIDPageRegex.FindStringSubmatch(pageHTML); len(m) > 1 {
			c.session.SessionID = m[1]
		}
	}

	records := ParsePurchaseHistoryHTML(pageHTML)
	cursor := extractHistoryCursor(pageHTML)

	for page := 1; page < maxPages && cursor != nil; page++ {
		form := url.Values{}
		form.Set("cursor[wallet_txnid]", fmt.Sprintf("%v", cursor.WalletTxnID))
		form.Set("cursor[timestamp_newest]", strconv.FormatInt(cursor.TimestampNewest, 10))
		form.Set("cursor[balance]", fmt.Sprintf("%v", cursor.Balance))
		form.Set("cursor[currency]", strconv.Itoa(cursor.Currency))

		moreBytes, err := c.PostStoreForm(ctx, "/account/AjaxLoadMoreHistory", form)
		if err != nil {
			break
		}
		var ajaxResp struct {
			HTML   string         `json:"html"`
			Cursor *historyCursor `json:"cursor"`
		}
		if err := json.Unmarshal(moreBytes, &ajaxResp); err != nil || strings.TrimSpace(ajaxResp.HTML) == "" {
			break
		}
		moreRecords := ParsePurchaseHistoryHTML(ajaxResp.HTML)
		if len(moreRecords) == 0 {
			break
		}
		records = append(records, moreRecords...)
		cursor = ajaxResp.Cursor
	}

	return records, nil
}

// FetchAccountLicenses retrieves all package activations (including Retail Keys, Complimentary/Free, Gifts) from /account/licenses/.
func (c *Client) FetchAccountLicenses(ctx context.Context) ([]models.LicenseRecord, error) {
	body, err := c.GetStore(ctx, "/account/licenses/")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch account licenses: %w", err)
	}
	return ParseAccountLicensesHTML(string(body)), nil
}

var (
	sessionIDPageRegex = regexp.MustCompile(`g_sessionID\s*=\s*"([^"]+)"`)
	cursorPageRegex    = regexp.MustCompile(`g_historyCursor\s*=\s*(\{[^;]+\});`)
	walletRowRegex     = regexp.MustCompile(`(?s)<tr[^>]*class="[^"]*wallet_table_row[^"]*"[^>]*>(.*?)</tr>`)
	whtDateRegex       = regexp.MustCompile(`(?s)<td[^>]*class="[^"]*wht_date[^"]*"[^>]*>(.*?)</td>`)
	whtItemsRegex      = regexp.MustCompile(`(?s)<td[^>]*class="[^"]*wht_items[^"]*"[^>]*>(.*?)</td>`)
	whtTypeRegex       = regexp.MustCompile(`(?s)<td[^>]*class="[^"]*wht_type[^"]*"[^>]*>(.*?)</td>`)
	whtTotalRegex      = regexp.MustCompile(`(?s)<td[^>]*class="[^"]*wht_total[^"]*"[^>]*>(.*?)</td>`)
	itemDivRegex       = regexp.MustCompile(`(?s)<div[^>]*>(.*?)</div>`)
	htmlTagRegex       = regexp.MustCompile(`<[^>]+>`)
	priceDigitsRegex   = regexp.MustCompile(`([0-9]+)[.,]([0-9]{2})`)
	txnIDRegex         = regexp.MustCompile(`transid=([0-9]+)`)

	licenseRowRegex  = regexp.MustCompile(`(?s)<tr[^>]*>\s*<td[^>]*class="license_date_col"[^>]*>(.*?)</td>\s*<td[^>]*>(.*?)</td>\s*<td[^>]*class="license_acquisition_col"[^>]*>(.*?)</td>\s*</tr>`)
	removeLinkRegex  = regexp.MustCompile(`(?s)<div[^>]*class="free_license_remove_link"[^>]*>.*?</div>`)
)

func extractHistoryCursor(pageHTML string) *historyCursor {
	m := cursorPageRegex.FindStringSubmatch(pageHTML)
	if len(m) < 2 {
		return nil
	}
	var c historyCursor
	if err := json.Unmarshal([]byte(m[1]), &c); err != nil || c.TimestampNewest == 0 {
		return nil
	}
	return &c
}

// ParsePurchaseHistoryHTML extracts PurchaseRecord entries from Steam account history HTML.
func ParsePurchaseHistoryHTML(rawHTML string) []models.PurchaseRecord {
	var records []models.PurchaseRecord
	rows := walletRowRegex.FindAllStringSubmatch(rawHTML, -1)

	// Collect refunded item names so we can mark the corresponding purchase row as refunded
	refundedTitles := make(map[string]bool)

	type rawParsedRow struct {
		rec models.PurchaseRecord
	}
	var tempRows []rawParsedRow

	for _, rowMatch := range rows {
		rowFull := rowMatch[0]
		rowInner := rowMatch[1]

		var rec models.PurchaseRecord
		if tm := txnIDRegex.FindStringSubmatch(rowFull); len(tm) > 1 {
			rec.TransactionID = tm[1]
		}

		if dm := whtDateRegex.FindStringSubmatch(rowInner); len(dm) > 1 {
			dateStr := cleanHTMLText(dm[1])
			rec.Date = parseSteamDate(dateStr)
		}

		if im := whtItemsRegex.FindStringSubmatch(rowInner); len(im) > 1 {
			itemsCell := im[1]
			divs := itemDivRegex.FindAllStringSubmatch(itemsCell, -1)
			if len(divs) > 0 {
				for _, d := range divs {
					txt := cleanHTMLText(d[1])
					if txt != "" && !strings.HasPrefix(strings.ToLower(txt), "steambundle") {
						rec.Items = append(rec.Items, txt)
					}
				}
			}
			if len(rec.Items) == 0 {
				txt := cleanHTMLText(itemsCell)
				if txt != "" {
					rec.Items = append(rec.Items, txt)
				}
			}
		}

		// Filter out wallet credit additions and Steam Community Market rows that are not game purchases
		if len(rec.Items) == 0 || isNonGameTransaction(rec.Items) {
			continue
		}

		if tym := whtTypeRegex.FindStringSubmatch(rowInner); len(tym) > 1 {
			rec.TypeDescription = cleanHTMLText(tym[1])
		}

		if totm := whtTotalRegex.FindStringSubmatch(rowInner); len(totm) > 1 {
			totalRaw := totm[1]
			if strings.Contains(totalRaw, "wht_refunded") {
				rec.IsRefunded = true
			}
			rec.PriceDisplay = cleanHTMLText(totalRaw)
			rec.PriceCents = parsePriceCents(rec.PriceDisplay)
		}

		if strings.Contains(strings.ToLower(rec.TypeDescription), "refund") {
			rec.IsRefunded = true
			for _, it := range rec.Items {
				refundedTitles[NormalizeTitle(it)] = true
			}
		}

		rec.IsBundle = len(rec.Items) > 1 || containsBundleKeyword(rec.Items)
		rec.IsFree = rec.PriceCents == 0 && !rec.IsRefunded
		rec.AcquisitionType = classifyPurchaseAcquisition(rec)

		tempRows = append(tempRows, rawParsedRow{rec: rec})
	}

	for _, r := range tempRows {
		rec := r.rec
		if !rec.IsRefunded {
			for _, it := range rec.Items {
				if refundedTitles[NormalizeTitle(it)] {
					rec.IsRefunded = true
					rec.AcquisitionType = models.AcquisitionRefunded
					break
				}
			}
		}
		records = append(records, rec)
	}

	return records
}

// ParseAccountLicensesHTML extracts LicenseRecord entries from Steam's /account/licenses/ HTML.
func ParseAccountLicensesHTML(rawHTML string) []models.LicenseRecord {
	var records []models.LicenseRecord
	matches := licenseRowRegex.FindAllStringSubmatch(rawHTML, -1)
	for _, m := range matches {
		dateStr := cleanHTMLText(m[1])
		itemRaw := removeLinkRegex.ReplaceAllString(m[2], "")
		itemName := cleanHTMLText(itemRaw)
		method := cleanHTMLText(m[3])
		if itemName == "" {
			continue
		}

		rec := models.LicenseRecord{
			Date:              parseSteamDate(dateStr),
			ItemName:          itemName,
			AcquisitionMethod: method,
			AcquisitionType:   classifyLicenseMethod(method, itemName),
		}
		records = append(records, rec)
	}
	return records
}

func classifyPurchaseAcquisition(rec models.PurchaseRecord) models.AcquisitionType {
	if rec.IsRefunded {
		return models.AcquisitionRefunded
	}
	descLower := strings.ToLower(rec.TypeDescription)
	if strings.Contains(descLower, "gift") {
		return models.AcquisitionGift
	}
	if rec.IsBundle {
		return models.AcquisitionBundle
	}
	if rec.IsFree {
		return models.AcquisitionFree
	}
	return models.AcquisitionPurchase
}

func classifyLicenseMethod(method, itemName string) models.AcquisitionType {
	mLower := strings.ToLower(method)
	iLower := strings.ToLower(itemName)
	switch {
	case strings.Contains(mLower, "complimentary") || strings.Contains(mLower, "free"):
		return models.AcquisitionFree
	case strings.Contains(mLower, "gift") || strings.Contains(mLower, "guest pass"):
		return models.AcquisitionGift
	case strings.Contains(mLower, "retail"):
		if strings.Contains(iLower, "bundle") || strings.Contains(iLower, "collection") || strings.Contains(iLower, "humble") {
			return models.AcquisitionBundle
		}
		return models.AcquisitionRetailKey
	case strings.Contains(mLower, "steam store"):
		if strings.Contains(iLower, "bundle") {
			return models.AcquisitionBundle
		}
		return models.AcquisitionPurchase
	default:
		return models.AcquisitionUnknown
	}
}

func isNonGameTransaction(items []string) bool {
	if len(items) == 1 {
		lower := strings.ToLower(strings.TrimSpace(items[0]))
		if lower == "steam community market" ||
			strings.HasPrefix(lower, "purchased ") && strings.Contains(lower, "wallet credit") {
			return true
		}
	}
	return false
}

func containsBundleKeyword(items []string) bool {
	for _, it := range items {
		lower := strings.ToLower(it)
		if strings.Contains(lower, "bundle") || strings.Contains(lower, "pack") || strings.Contains(lower, "collection") {
			return true
		}
	}
	return false
}

func cleanHTMLText(s string) string {
	noTags := htmlTagRegex.ReplaceAllString(s, " ")
	unescaped := html.UnescapeString(noTags)
	fields := strings.Fields(unescaped)
	return strings.TrimSpace(strings.Join(fields, " "))
}

func parsePriceCents(priceStr string) int {
	lower := strings.ToLower(strings.TrimSpace(priceStr))
	if lower == "" || strings.Contains(lower, "free") || lower == "--" {
		return 0
	}
	m := priceDigitsRegex.FindStringSubmatch(priceStr)
	if len(m) < 3 {
		return 0
	}
	dollars, _ := strconv.Atoi(m[1])
	cents, _ := strconv.Atoi(m[2])
	return dollars*100 + cents
}

var steamDateFormats = []string{
	"Jan 2, 2006",
	"2 Jan, 2006",
	"Jan 02, 2006",
	"02 Jan, 2006",
	"2006-01-02",
	"2 Jan 2006",
	"Jan 2 2006",
}

func parseSteamDate(dateStr string) time.Time {
	s := strings.TrimSpace(dateStr)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range steamDateFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	// If year is omitted (Steam sometimes omits the current year like "Oct 15" or "15 Oct"), append current year
	currentYear := time.Now().Year()
	for _, shortLayout := range []string{"Jan 2, 2006", "2 Jan, 2006"} {
		withYear := fmt.Sprintf("%s, %d", s, currentYear)
		if t, err := time.Parse(shortLayout, withYear); err == nil {
			return t
		}
	}
	return time.Time{}
}

var nonAlnumRegex = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeTitle normalizes a game or package title for fuzzy matching between store purchases and library items.
func NormalizeTitle(title string) string {
	s := strings.ToLower(title)
	// Strip common suffixes in license/purchase names
	for _, suffix := range []string{
		" - steam store and retail key",
		" steam store and retail key",
		" retail",
		" key",
		" digital",
		" edition",
		" row",
		" (row)",
		" ww",
		"pre-purchase ",
		"pre-order ",
	} {
		s = strings.ReplaceAll(s, suffix, "")
	}
	s = strings.ReplaceAll(s, "™", "")
	s = strings.ReplaceAll(s, "®", "")
	s = strings.ReplaceAll(s, "©", "")
	return strings.TrimSpace(nonAlnumRegex.ReplaceAllString(s, " "))
}

