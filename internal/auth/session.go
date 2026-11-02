package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const steamID64Base uint64 = 76561197960265728

// Session stores the user's authenticated Steam web cookies, tokens, and identity.
type Session struct {
	SteamID64        string            `json:"steam_id64"`
	AccountID        uint32            `json:"account_id"`
	PersonaName      string            `json:"persona_name,omitempty"`
	SteamLoginSecure string            `json:"steam_login_secure"`
	SessionID        string            `json:"session_id,omitempty"`
	WebAPIToken      string            `json:"webapi_token,omitempty"`
	APIKey           string            `json:"api_key,omitempty"`
	Cookies          map[string]string `json:"cookies,omitempty"`
	ExpiresAt        time.Time         `json:"expires_at"`
	CreatedAt        time.Time         `json:"created_at"`
}

// ConfigDir returns the application data directory (~/.steam-organizer).
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".steam-organizer")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}
	return dir, nil
}

// SessionFilePath returns the path to the saved session file.
func SessionFilePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session.json"), nil
}

// LoadSession loads the cached Steam session from disk.
func LoadSession() (*Session, error) {
	path, err := SessionFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid session file: %w", err)
	}
	if apiKey := os.Getenv("STEAM_API_KEY"); apiKey != "" && s.APIKey == "" {
		s.APIKey = apiKey
	}
	return &s, nil
}

// Save persists the session to disk with restricted permissions.
func (s *Session) Save() error {
	path, err := SessionFilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ClearSession removes the saved session file.
func ClearSession() error {
	path, err := SessionFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// IsValid checks whether the session has required credentials and is not expired.
func (s *Session) IsValid() bool {
	if s == nil || s.SteamLoginSecure == "" || s.SteamID64 == "" {
		return false
	}
	if !s.ExpiresAt.IsZero() && time.Now().After(s.ExpiresAt.Add(-2*time.Minute)) {
		return false
	}
	return true
}

// SteamID64ToAccountID converts a 64-bit SteamID string to a 32-bit Steam3 AccountID.
func SteamID64ToAccountID(steamID64Str string) (uint32, error) {
	val, err := strconv.ParseUint(strings.TrimSpace(steamID64Str), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid SteamID64 %q: %w", steamID64Str, err)
	}
	if val <= steamID64Base {
		return 0, fmt.Errorf("SteamID64 %d is below base offset", val)
	}
	return uint32(val - steamID64Base), nil
}

// AccountIDToSteamID64 converts a 32-bit Steam3 AccountID to a 64-bit SteamID string.
func AccountIDToSteamID64(accountID uint32) string {
	return strconv.FormatUint(steamID64Base+uint64(accountID), 10)
}

// ParseSteamLoginSecure extracts the SteamID64, access token, and JWT expiration from steamLoginSecure.
func ParseSteamLoginSecure(rawCookie string) (steamID64 string, jwtToken string, expiresAt time.Time, err error) {
	decoded, err := url.QueryUnescape(strings.TrimSpace(rawCookie))
	if err != nil {
		decoded = strings.TrimSpace(rawCookie)
	}
	parts := strings.SplitN(decoded, "||", 2)
	if len(parts) != 2 {
		return "", "", time.Time{}, errors.New("steamLoginSecure cookie must have format '<steamid64>||<jwt>'")
	}
	steamID64 = strings.TrimSpace(parts[0])
	jwtToken = strings.TrimSpace(parts[1])

	if _, err := strconv.ParseUint(steamID64, 10, 64); err != nil {
		return "", "", time.Time{}, fmt.Errorf("invalid SteamID64 in cookie: %w", err)
	}

	// Parse JWT payload for expiration time (`exp`)
	jwtSegments := strings.Split(jwtToken, ".")
	if len(jwtSegments) >= 2 {
		payloadBytes, decErr := base64.RawURLEncoding.DecodeString(jwtSegments[1])
		if decErr == nil {
			var claims struct {
				Exp int64  `json:"exp"`
				Sub string `json:"sub"`
			}
			if json.Unmarshal(payloadBytes, &claims) == nil && claims.Exp > 0 {
				expiresAt = time.Unix(claims.Exp, 0)
			}
		}
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(24 * time.Hour)
	}
	return steamID64, jwtToken, expiresAt, nil
}

