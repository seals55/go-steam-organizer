package steam

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/models"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newHandlerHTTPClient(h http.Handler) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec.Result(), nil
		}),
	}
}

func TestClientFullSyncAndMetadataEnrichment(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOME", tmpHome)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/IPlayerService/GetOwnedGames/v1/":
			_, _ = fmt.Fprint(w, `{
				"response": {
					"game_count": 2,
					"games": [
						{"appid": 1145350, "name": "Hades II", "playtime_forever": 240, "rtime_last_played": 1730000000},
						{"appid": 620, "name": "Portal 2", "playtime_forever": 0, "rtime_last_played": 0}
					]
				}
			}`)

		case "/dynamicstore/userdata/":
			_, _ = fmt.Fprint(w, `{"rgOwnedApps":[1145350, 620, 440],"rgOwnedPackages":[1,2]}`)

		case "/account/history/":
			_, _ = fmt.Fprint(w, `
				<script>var g_sessionID = "sess_abc"; var g_historyCursor = {"wallet_txnid":"100","timestamp_newest":1730000000,"balance":"0","currency":1};</script>
				<table class="wallet_history_table">
					<tr class="wallet_table_row">
						<td class="wht_date">Nov 24, 2025</td>
						<td class="wht_items"><div>Hades II</div></td>
						<td class="wht_type"><div>Purchase</div></td>
						<td class="wht_total">$29.99</td>
					</tr>
				</table>
			`)

		case "/account/AjaxLoadMoreHistory":
			_, _ = fmt.Fprint(w, `{
				"html": "<tr class=\"wallet_table_row\"><td class=\"wht_date\">Dec 20, 2024</td><td class=\"wht_items\"><div>Portal 2</div></td><td class=\"wht_type\"><div>Gift</div></td><td class=\"wht_total\">$9.99</td></tr>",
				"cursor": null
			}`)

		case "/account/licenses/":
			_, _ = fmt.Fprint(w, sampleLicensesHTML)

		case "/api/appdetails":
			appID := r.URL.Query().Get("appids")
			_, _ = fmt.Fprintf(w, `{
				"%s": {
					"success": true,
					"data": {
						"name": "Enriched App %s",
						"type": "game",
						"is_free": false,
						"developers": ["Supergiant Games"],
						"publishers": ["Supergiant Games"],
						"genres": [{"id": "1", "description": "Action"}, {"id": "3", "description": "RPG"}],
						"categories": [{"id": 2, "description": "Single-player"}, {"id": 28, "description": "Full controller support"}],
						"price_overview": {"final": 2999}
					}
				}
			}`, appID, appID)

		case "/saleaction/ajaxgetdeckappcompatibilityreport":
			_, _ = fmt.Fprint(w, `{"success": 1, "results": {"resolved_category": 3}}`)

		case "/apphoverpublic/1145350", "/apphoverpublic/620", "/apphoverpublic/440":
			_, _ = fmt.Fprint(w, `<div class="app_tag">Roguelike</div><div class="app_tag">Action</div>`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	sess := &auth.Session{
		SteamID64:        "76561198000000001",
		AccountID:        39734273,
		SteamLoginSecure: "76561198000000001||token",
		WebAPIToken:      "token",
		Cookies:          map[string]string{"steamCountry": "US"},
		ExpiresAt:        time.Now().Add(time.Hour),
	}

	client := NewClient(sess)
	client.SetHTTPClient(newHandlerHTTPClient(handler))

	cache, err := client.SyncFullLibrary(t.Context(), true, 5, func(string) {})
	if err != nil {
		t.Fatalf("SyncFullLibrary failed: %v", err)
	}
	if len(cache.Items) != 3 {
		t.Fatalf("expected 3 owned items (2 from API + 1 from dynamicstore), got %d", len(cache.Items))
	}
	if len(cache.Purchases) != 2 {
		t.Fatalf("expected 2 paginated purchases, got %d", len(cache.Purchases))
	}

	// Verify cached reload works without hitting network
	cachedReload, err := client.SyncFullLibrary(t.Context(), false, 5, nil)
	if err != nil || len(cachedReload.Items) != 3 {
		t.Fatalf("expected cached reload of 3 items, got err=%v", err)
	}

	// Enrich metadata
	enriched := client.EnrichItems(t.Context(), cache.Items, true, 10, func(string) {})
	for _, it := range enriched {
		if it.DeckCompatibility != models.DeckVerified {
			t.Errorf("expected DeckVerified for AppID %d, got %s", it.AppID, it.DeckCompatibility)
		}
		if len(it.Genres) != 2 || len(it.Tags) != 2 {
			t.Errorf("expected genres and tags on AppID %d: %+v", it.AppID, it)
		}
	}

	// Verify metadata store disk cache hit
	ms := LoadMetadataStore()
	if _, ok := ms.Get(1145350); !ok {
		t.Errorf("expected AppID 1145350 to be saved in MetadataStore disk cache")
	}
}

func TestParseCommunityGamesHTML(t *testing.T) {
	html := `<script>var rgGames = [{"appid":730,"name":"Counter-Strike 2","hours_forever":"12.5","last_played":1700000000}];</script>`
	items := parseCommunityGamesHTML(html)
	if len(items) != 1 || items[0].AppID != 730 || items[0].PlaytimeMinutes != 750 {
		t.Errorf("unexpected community games parse result: %+v", items)
	}
}
