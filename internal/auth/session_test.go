package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

