package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/models"
)

// Client interacts with Steam Web APIs and Store endpoints using an authenticated Session.
type Client struct {
	session    *auth.Session
	httpClient *http.Client
	baseStore  string
	baseAPI    string
}

// NewClient creates a new Steam client from an authenticated session.
func NewClient(sess *auth.Session) *Client {
	return &Client{
		session: sess,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseStore: "https://store.steampowered.com",
		baseAPI:   "https://api.steampowered.com",
	}
}

// Session returns the underlying auth session.
func (c *Client) Session() *auth.Session {
	return c.session
}

// SetHTTPClient overrides the underlying HTTP client (useful for testing with custom RoundTrippers).
func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc != nil {
		c.httpClient = hc
	}
}

func (c *Client) attachCookies(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if c.session == nil {
		return
	}
	req.AddCookie(&http.Cookie{Name: "steamLoginSecure", Value: c.session.SteamLoginSecure})
	if c.session.SessionID != "" {
		req.AddCookie(&http.Cookie{Name: "sessionid", Value: c.session.SessionID})
	}
	req.AddCookie(&http.Cookie{Name: "birthtime", Value: "631152001"})
	req.AddCookie(&http.Cookie{Name: "lastagecheckage", Value: "1-January-1990"})
	for k, v := range c.session.Cookies {
		if k != "steamLoginSecure" && k != "sessionid" {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}
	}
}

// GetStore executes an authenticated GET request against store.steampowered.com (or full URL).
func (c *Client) GetStore(ctx context.Context, pathOrURL string) ([]byte, error) {
	target := pathOrURL
	if !strings.HasPrefix(target, "http") {
		target = c.baseStore + pathOrURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	c.attachCookies(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, target)
	}
	if resp.Request != nil && resp.Request.URL != nil {
		finalPath := strings.ToLower(resp.Request.URL.Path)
		reqLower := strings.ToLower(pathOrURL)
		if strings.Contains(finalPath, "/login") && !strings.Contains(reqLower, "/login") {
			return nil, fmt.Errorf("Steam Store session expired or unauthorized (redirected to %s); run 'steam-organizer login --force' to re-authenticate", resp.Request.URL.Path)
		}
	}
	return io.ReadAll(resp.Body)
}

// PostStoreForm executes an authenticated POST form request against store.steampowered.com.
func (c *Client) PostStoreForm(ctx context.Context, pathOrURL string, form url.Values) ([]byte, error) {
	target := pathOrURL
	if !strings.HasPrefix(target, "http") {
		target = c.baseStore + pathOrURL
	}
	if c.session != nil && c.session.SessionID != "" && form.Get("sessionid") == "" {
		form.Set("sessionid", c.session.SessionID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	c.attachCookies(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", c.baseStore+"/account/history/")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, target)
	}
	if resp.Request != nil && resp.Request.URL != nil {
		finalPath := strings.ToLower(resp.Request.URL.Path)
		reqLower := strings.ToLower(pathOrURL)
		if strings.Contains(finalPath, "/login") && !strings.Contains(reqLower, "/login") {
			return nil, fmt.Errorf("Steam Store session expired or unauthorized (redirected to %s); run 'steam-organizer login --force' to re-authenticate", resp.Request.URL.Path)
		}
	}
	return io.ReadAll(resp.Body)
}

// GetWebAPI calls api.steampowered.com using either access_token or key.
func (c *Client) GetWebAPI(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	if c.session != nil {
		if params.Get("access_token") == "" && params.Get("key") == "" {
			if c.session.APIKey != "" {
				params.Set("key", c.session.APIKey)
			} else if c.session.WebAPIToken != "" {
				params.Set("access_token", c.session.WebAPIToken)
			}
		}
	}

	target := fmt.Sprintf("%s/%s?%s", c.baseAPI, strings.TrimPrefix(endpoint, "/"), params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	c.attachCookies(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Steam Web API %s returned HTTP %d", endpoint, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// LibraryCache stores cached library items and purchase records on disk.
type LibraryCache struct {
	SteamID64 string                  `json:"steam_id64"`
	UpdatedAt time.Time               `json:"updated_at"`
	Items     []models.LibraryItem    `json:"items"`
	Purchases []models.PurchaseRecord `json:"purchases"`
	Licenses  []models.LicenseRecord  `json:"licenses"`
}

func libraryCachePath() (string, error) {
	dir, err := auth.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache", "library_cache.json"), nil
}

// LoadLibraryCache reads the cached library & purchases snapshot if available.
func LoadLibraryCache(steamID64 string) (*LibraryCache, error) {
	path, err := libraryCachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache LibraryCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	if steamID64 != "" && cache.SteamID64 != steamID64 {
		return nil, fmt.Errorf("cache belongs to different SteamID")
	}
	return &cache, nil
}

// SaveLibraryCache writes the library & purchases snapshot to disk.
func SaveLibraryCache(cache *LibraryCache) error {
	path, err := libraryCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

