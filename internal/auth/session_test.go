package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func makeTestJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(map[string]any{
		"sub": "76561198000000001",
		"exp": exp,
	})
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return fmt.Sprintf("%s.%s.signature", header, payload)
}

func TestSteamIDConversions(t *testing.T) {
	steamID := "76561198000000001"
	accID, err := SteamID64ToAccountID(steamID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accID != 39734273 {
		t.Errorf("expected AccountID 39734273, got %d", accID)
	}
	roundTrip := AccountIDToSteamID64(accID)
	if roundTrip != steamID {
		t.Errorf("expected round-trip %s, got %s", steamID, roundTrip)
	}

	if _, err := SteamID64ToAccountID("invalid"); err == nil {
		t.Errorf("expected error for non-numeric SteamID64")
	}
	if _, err := SteamID64ToAccountID("100"); err == nil {
		t.Errorf("expected error for SteamID64 below base offset")
	}
}

func TestParseSteamLoginSecure(t *testing.T) {
	futureExp := time.Now().Add(6 * time.Hour).Unix()
	jwt := makeTestJWT(futureExp)
	rawCookie := "76561198000000001%7C%7C" + jwt

	steamID64, token, exp, err := ParseSteamLoginSecure(rawCookie)
	if err != nil {
		t.Fatalf("ParseSteamLoginSecure failed: %v", err)
	}
	if steamID64 != "76561198000000001" {
		t.Errorf("unexpected steamID64 %q", steamID64)
	}
	if token != jwt {
		t.Errorf("unexpected token %q", token)
	}
	if exp.Unix() != futureExp {
		t.Errorf("expected exp %d, got %d", futureExp, exp.Unix())
	}

	if _, _, _, err := ParseSteamLoginSecure("badcookie"); err == nil {
		t.Errorf("expected error for missing '||' separator")
	}
	if _, _, _, err := ParseSteamLoginSecure("notanumber||token"); err == nil {
		t.Errorf("expected error for non-numeric steamid")
	}

	// Verify non-store subdomain JWTs (like checkout.steampowered.com with aud=["web:checkout"]) are rejected
	checkoutPayload, _ := json.Marshal(map[string]any{
		"sub": "76561198000000001",
		"exp": futureExp,
		"aud": []string{"web:checkout"},
	})
	checkoutJWT := fmt.Sprintf("eyJhbGciOiJFZERTQSJ9.%s.sig", base64.RawURLEncoding.EncodeToString(checkoutPayload))
	if _, _, _, err := ParseSteamLoginSecure("76561198000000001||" + checkoutJWT); err == nil {
		t.Errorf("expected error for non-store audience web:checkout")
	}
	checkoutSess := &Session{
		SteamID64:        "76561198000000001",
		SteamLoginSecure: "76561198000000001||" + checkoutJWT,
		ExpiresAt:        time.Unix(futureExp, 0),
	}
	if checkoutSess.IsValid() {
		t.Errorf("expected session with web:checkout audience to be invalid")
	}

	// Verify store.steampowered.com JWT with aud=["web:store"] is accepted
	storePayload, _ := json.Marshal(map[string]any{
		"sub": "76561198000000001",
		"exp": futureExp,
		"aud": []string{"web:store"},
	})
	storeJWT := fmt.Sprintf("eyJhbGciOiJFZERTQSJ9.%s.sig", base64.RawURLEncoding.EncodeToString(storePayload))
	if _, _, _, err := ParseSteamLoginSecure("76561198000000001||" + storeJWT); err != nil {
		t.Errorf("expected web:store audience to succeed, got %v", err)
	}

	// Verify extractStoreCookies ignores checkout/login subdomain steamLoginSecure cookies
	cookies := extractStoreCookies([]cdpCookie{
		{Name: "steamLoginSecure", Value: "store_cookie_val", Domain: "store.steampowered.com"},
		{Name: "sessionid", Value: "store_sess_id", Domain: "store.steampowered.com"},
		{Name: "steamLoginSecure", Value: "checkout_cookie_val", Domain: "checkout.steampowered.com"},
		{Name: "sessionid", Value: "checkout_sess_id", Domain: "checkout.steampowered.com"},
		{Name: "browserid", Value: "shared_browser_id", Domain: ".steampowered.com"},
	})
	if cookies["steamLoginSecure"] != "store_cookie_val" || cookies["sessionid"] != "store_sess_id" || cookies["browserid"] != "shared_browser_id" {
		t.Errorf("unexpected extracted store cookies: %+v", cookies)
	}
}

func TestSessionLifecycleAndValidity(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOME", tmpHome)
	t.Setenv("STEAM_API_KEY", "test_env_key_123")

	var nilSess *Session
	if nilSess.IsValid() {
		t.Errorf("nil session should not be valid")
	}

	expiredSess := &Session{
		SteamID64:        "76561198000000001",
		SteamLoginSecure: "76561198000000001||tok",
		ExpiresAt:        time.Now().Add(-10 * time.Minute),
	}
	if expiredSess.IsValid() {
		t.Errorf("expired session should not be valid")
	}

	validSess := &Session{
		SteamID64:        "76561198000000001",
		AccountID:        39734273,
		PersonaName:      "Tester",
		SteamLoginSecure: "76561198000000001||tok",
		ExpiresAt:        time.Now().Add(2 * time.Hour),
		CreatedAt:        time.Now(),
	}
	if !validSess.IsValid() {
		t.Errorf("expected session to be valid")
	}

	if err := validSess.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := LoadSession()
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	if loaded.PersonaName != "Tester" || loaded.APIKey != "test_env_key_123" {
		t.Errorf("unexpected loaded session: %+v", loaded)
	}

	// EnsureAuthenticated should return cached valid session without launching browser
	ensured, err := EnsureAuthenticated(t.Context(), LoginOptions{Force: false, APIKey: "override_key"})
	if err != nil {
		t.Fatalf("EnsureAuthenticated failed: %v", err)
	}
	if ensured.APIKey != "override_key" {
		t.Errorf("expected updated APIKey override_key, got %q", ensured.APIKey)
	}

	if err := ClearSession(); err != nil {
		t.Fatalf("ClearSession failed: %v", err)
	}
	if _, err := LoadSession(); err == nil {
		t.Errorf("expected error loading session after ClearSession")
	}
}

type mockRoundTripper func(req *http.Request) (*http.Response, error)

func (f mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPortalMuxAndEnrichSessionMetadata(t *testing.T) {
	sessCh := make(chan *Session, 1)
	mux := newPortalMux(sessCh)

	// 1. GET / should render portalHTML
	getReq := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), "go-steam-organizer Login") {
		t.Errorf("unexpected GET / response: code=%d", getRec.Code)
	}

	// 2. POST / with invalid cookie should return 400
	badForm := url.Values{"steamLoginSecure": {"invalid"}}
	badReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(badForm.Encode()))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badRec := httptest.NewRecorder()
	mux.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid cookie, got %d", badRec.Code)
	}

	// 3. POST / with valid steamLoginSecure cookie should send *Session to sessCh
	jwt := makeTestJWT(time.Now().Add(4 * time.Hour).Unix())
	validForm := url.Values{
		"steamLoginSecure": {"76561198000000001||" + jwt},
		"sessionid":        {"sess_abc"},
		"apiKey":           {"api_xyz"},
	}
	postReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(validForm.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder()
	mux.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on valid POST, got %d: %s", postRec.Code, postRec.Body.String())
	}

	var captured *Session
	select {
	case captured = <-sessCh:
	default:
		t.Fatal("expected session on sessCh")
	}
	if captured.SteamID64 != "76561198000000001" || captured.SessionID != "sess_abc" || captured.APIKey != "api_xyz" {
		t.Errorf("unexpected captured session: %+v", captured)
	}

	// 4. Test enrichSessionMetadata with mock HTTP transport
	origClient := authHTTPClient
	defer func() { authHTTPClient = origClient }()

	authHTTPClient = &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			if strings.Contains(req.URL.Path, "ajaxgetasyncconfig") {
				_, _ = fmt.Fprint(rec, `{"success":1,"data":{"webapi_token":"enriched_webapi_token"}}`)
			} else if strings.Contains(req.URL.Path, "/account/") {
				http.SetCookie(rec, &http.Cookie{Name: "sessionid", Value: "from_account_cookie"})
				_, _ = fmt.Fprint(rec, `<html><body><span id="account_pulldown">EnrichedPlayer</span></body></html>`)
			}
			return rec.Result(), nil
		}),
	}

	captured.SessionID = ""
	captured.Cookies["custom_cookie"] = "val"
	enrichSessionMetadata(t.Context(), captured)
	if captured.WebAPIToken != "enriched_webapi_token" || captured.PersonaName != "EnrichedPlayer" || captured.SessionID != "from_account_cookie" {
		t.Errorf("unexpected enriched session: %+v", captured)
	}

	// Test findChromiumBrowser (smoke check)
	_ = findChromiumBrowser()
}


