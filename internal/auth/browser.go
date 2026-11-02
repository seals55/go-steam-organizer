package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

const (
	steamLoginURL      = "https://store.steampowered.com/login/"
	steamAccountURL    = "https://store.steampowered.com/account/"
	steamAsyncCfgURL   = "https://store.steampowered.com/pointssummary/ajaxgetasyncconfig"
	defaultLoginTimout = 5 * time.Minute
)

// LoginOptions configures the browser authentication process.
type LoginOptions struct {
	Force       bool
	Manual      bool
	APIKey      string
	Timeout     time.Duration
	LogProgress func(msg string)
}

// EnsureAuthenticated returns a valid Steam Session, prompting the user to log in via the browser if necessary.
func EnsureAuthenticated(ctx context.Context, opts LoginOptions) (*Session, error) {
	if !opts.Force {
		if sess, err := LoadSession(); err == nil && sess.IsValid() {
			if opts.APIKey != "" && sess.APIKey != opts.APIKey {
				sess.APIKey = opts.APIKey
				_ = sess.Save()
			}
			return sess, nil
		}
	}
	return LoginViaBrowser(ctx, opts)
}

// LoginViaBrowser initiates an interactive browser login to Steam and captures the session credentials.
func LoginViaBrowser(ctx context.Context, opts LoginOptions) (*Session, error) {
	if opts.Timeout == 0 {
		opts.Timeout = defaultLoginTimout
	}
	logf := func(format string, args ...any) {
		if opts.LogProgress != nil {
			opts.LogProgress(fmt.Sprintf(format, args...))
		} else {
			fmt.Printf(format+"\n", args...)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var sess *Session
	var err error

	if !opts.Manual {
		browserBin := findChromiumBrowser()
		if browserBin != "" {
			logf("Opening Steam login in browser (%s)...", filepath.Base(browserBin))
			logf("Please sign in to your Steam account in the opened browser window.")
			sess, err = loginWithCDP(ctx, browserBin, logf)
			if err != nil {
				logf("Automated browser login did not complete (%v). Switching to local browser helper...", err)
			}
		}
	}

	if sess == nil {
		sess, err = loginWithLocalPortal(ctx, logf)
		if err != nil {
			return nil, err
		}
	}

	if opts.APIKey != "" {
		sess.APIKey = opts.APIKey
	} else if envKey := os.Getenv("STEAM_API_KEY"); envKey != "" && sess.APIKey == "" {
		sess.APIKey = envKey
	}

	// Enrich session with webapi_token and persona name if not already populated
	enrichSessionMetadata(ctx, sess)

	if err := sess.Save(); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}
	logf("Successfully authenticated as SteamID %s (AccountID %d, %s).",
		sess.SteamID64, sess.AccountID, sess.PersonaName)
	return sess, nil
}

// findChromiumBrowser locates Chrome, Edge, or Brave on Windows, macOS, or Linux.
func findChromiumBrowser() string {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		progFiles := os.Getenv("ProgramFiles")
		progFilesX86 := os.Getenv("ProgramFiles(x86)")
		for _, base := range []string{progFiles, progFilesX86, localAppData} {
			if base == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
			)
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default:
		for _, bin := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if p, err := exec.LookPath(bin); err == nil {
				return p
			}
		}
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

func getFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// loginWithCDP launches a Chromium-based browser with remote debugging enabled and polls CDP for Steam cookies.
func loginWithCDP(ctx context.Context, browserPath string, logf func(string, ...any)) (*Session, error) {
	port, err := getFreePort()
	if err != nil {
		return nil, err
	}

	cfgDir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	profileDir := filepath.Join(cfgDir, "browser-profile")
	_ = os.MkdirAll(profileDir, 0700)

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		fmt.Sprintf("--user-data-dir=%s", profileDir),
		"--no-first-run",
		"--no-default-browser-check",
		fmt.Sprintf("--app=%s", steamLoginURL),
	}

	cmd := exec.CommandContext(ctx, browserPath, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start browser: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	wsURL, err := waitForCDPWebSocket(ctx, port)
	if err != nil {
		return nil, err
	}

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, errors.New("timed out waiting for Steam login in browser")
		case <-ticker.C:
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				return nil, errors.New("browser window was closed before login finished")
			}
			cookies, cdpErr := fetchCDPCookies(wsURL)
			if cdpErr != nil {
				// Target page may have navigated; refresh target WebSocket URL
				if newWS, wErr := waitForCDPWebSocket(ctx, port); wErr == nil {
					wsURL = newWS
				}
				continue
			}

			loginSecure := cookies["steamLoginSecure"]
			if loginSecure == "" {
				continue
			}

			steamID64, jwtToken, expiresAt, parseErr := ParseSteamLoginSecure(loginSecure)
			if parseErr != nil {
				continue
			}

			accountID, accErr := SteamID64ToAccountID(steamID64)
			if accErr != nil {
				continue
			}

			logf("Detected authenticated Steam session for %s! Finalizing...", steamID64)
			sess := &Session{
				SteamID64:        steamID64,
				AccountID:        accountID,
				SteamLoginSecure: loginSecure,
				SessionID:        cookies["sessionid"],
				WebAPIToken:      jwtToken,
				Cookies:          cookies,
				ExpiresAt:        expiresAt,
				CreatedAt:        time.Now(),
			}
			return sess, nil
		}
	}
}

func waitForCDPWebSocket(ctx context.Context, port int) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	listURL := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	versionURL := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		if resp, err := client.Get(listURL); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var targets []struct {
				Type                 string `json:"type"`
				URL                  string `json:"url"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			if json.Unmarshal(body, &targets) == nil {
				for _, t := range targets {
					if t.WebSocketDebuggerURL != "" && (t.Type == "page" || strings.Contains(t.URL, "steampowered.com")) {
						return t.WebSocketDebuggerURL, nil
					}
				}
			}
		}

		if resp, err := client.Get(versionURL); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var ver struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			if json.Unmarshal(body, &ver) == nil && ver.WebSocketDebuggerURL != "" {
				return ver.WebSocketDebuggerURL, nil
			}
		}

		time.Sleep(300 * time.Millisecond)
	}
	return "", errors.New("could not connect to browser remote debugging endpoint")
}

var cdpMsgID atomic.Int64

func fetchCDPCookies(wsURL string) (map[string]string, error) {
	origin := "http://127.0.0.1/"
	ws, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	_ = ws.SetDeadline(time.Now().Add(3 * time.Second))

	id := cdpMsgID.Add(1)
	req := map[string]any{
		"id":     id,
		"method": "Network.getAllCookies",
	}
	if err := websocket.JSON.Send(ws, req); err != nil {
		return nil, err
	}

	for {
		var resp struct {
			ID     int64 `json:"id"`
			Result struct {
				Cookies []struct {
					Name   string `json:"name"`
					Value  string `json:"value"`
					Domain string `json:"domain"`
				} `json:"cookies"`
			} `json:"result"`
		}
		if err := websocket.JSON.Receive(ws, &resp); err != nil {
			return nil, err
		}
		if resp.ID != id {
			continue
		}
		result := make(map[string]string)
		for _, c := range resp.Result.Cookies {
			if strings.Contains(c.Domain, "steampowered.com") {
				result[c.Name] = c.Value
			}
		}
		return result, nil
	}
}

// newPortalMux creates the HTTP handler for the local browser login callback portal.
func newPortalMux(sessCh chan<- *Session) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			rawCookie := strings.TrimSpace(r.FormValue("steamLoginSecure"))
			sessionID := strings.TrimSpace(r.FormValue("sessionid"))
			apiKey := strings.TrimSpace(r.FormValue("apiKey"))

			steamID64, jwtToken, expiresAt, parseErr := ParseSteamLoginSecure(rawCookie)
			if parseErr != nil {
				http.Error(w, "Invalid steamLoginSecure value: "+parseErr.Error(), http.StatusBadRequest)
				return
			}
			accountID, accErr := SteamID64ToAccountID(steamID64)
			if accErr != nil {
				http.Error(w, "Invalid SteamID64: "+accErr.Error(), http.StatusBadRequest)
				return
			}
			cookies := map[string]string{
				"steamLoginSecure": rawCookie,
			}
			if sessionID != "" {
				cookies["sessionid"] = sessionID
			}
			sess := &Session{
				SteamID64:        steamID64,
				AccountID:        accountID,
				SteamLoginSecure: rawCookie,
				SessionID:        sessionID,
				WebAPIToken:      jwtToken,
				APIKey:           apiKey,
				Cookies:          cookies,
				ExpiresAt:        expiresAt,
				CreatedAt:        time.Now(),
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:sans-serif;background:#171a21;color:#c7d5e0;padding:40px;text-align:center;">
				<h2 style="color:#66c0f4;">Steam Authentication Successful!</h2>
				<p>You can close this tab and return to <b>go-steam-organizer</b>.</p>
			</body></html>`)
			select {
			case sessCh <- sess:
			default:
			}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, portalHTML)
	})
	return mux
}

// loginWithLocalPortal opens a local callback web page and the Steam login page in the user's default browser.
func loginWithLocalPortal(ctx context.Context, logf func(string, ...any)) (*Session, error) {
	port, err := getFreePort()
	if err != nil {
		return nil, err
	}
	portalURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	sessCh := make(chan *Session, 1)
	mux := newPortalMux(sessCh)

	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}
	go func() {
		_ = srv.ListenAndServe()
	}()
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()

	logf("Opening browser login helper at %s ...", portalURL)
	_ = openSystemBrowser(portalURL)

	select {
	case <-ctx.Done():
		return nil, errors.New("login cancelled or timed out")
	case sess := <-sessCh:
		return sess, nil
	}
}

func openSystemBrowser(targetURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	case "darwin":
		cmd = exec.Command("open", targetURL)
	default:
		cmd = exec.Command("xdg-open", targetURL)
	}
	return cmd.Start()
}

var personaRegex = regexp.MustCompile(`class="persona_name_text_content"[^>]*>\s*([^<]+?)\s*<`)
var accountPulldownRegex = regexp.MustCompile(`id="account_pulldown"[^>]*>\s*([^<]+?)\s*<`)

var authHTTPClient = &http.Client{Timeout: 10 * time.Second}

// enrichSessionMetadata fetches the user's webapi_token and persona name from store.steampowered.com.
func enrichSessionMetadata(ctx context.Context, sess *Session) {
	client := authHTTPClient

	addCookies := func(req *http.Request) {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.AddCookie(&http.Cookie{Name: "steamLoginSecure", Value: sess.SteamLoginSecure})
		if sess.SessionID != "" {
			req.AddCookie(&http.Cookie{Name: "sessionid", Value: sess.SessionID})
		}
		for k, v := range sess.Cookies {
			if k != "steamLoginSecure" && k != "sessionid" {
				req.AddCookie(&http.Cookie{Name: k, Value: v})
			}
		}
	}

	// 1. Try ajaxgetasyncconfig to get official webapi_token
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, steamAsyncCfgURL, nil); err == nil {
		addCookies(req)
		if resp, err := client.Do(req); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var asyncCfg struct {
				Success int `json:"success"`
				Data    struct {
					WebAPIToken string `json:"webapi_token"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &asyncCfg) == nil && asyncCfg.Data.WebAPIToken != "" {
				sess.WebAPIToken = asyncCfg.Data.WebAPIToken
			}
		}
	}

	// 2. Fetch account page for PersonaName and sessionid cookie if missing
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, steamAccountURL, nil); err == nil {
		addCookies(req)
		if resp, err := client.Do(req); err == nil {
			for _, c := range resp.Cookies() {
				if c.Name == "sessionid" && sess.SessionID == "" {
					sess.SessionID = c.Value
				}
			}
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			html := string(bodyBytes)
			if m := accountPulldownRegex.FindStringSubmatch(html); len(m) > 1 {
				sess.PersonaName = strings.TrimSpace(m[1])
			} else if m := personaRegex.FindStringSubmatch(html); len(m) > 1 {
				sess.PersonaName = strings.TrimSpace(m[1])
			}
		}
	}
	if sess.PersonaName == "" {
		sess.PersonaName = "SteamUser_" + sess.SteamID64
	}
}

const portalHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Steam Organizer - Browser Login</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #1b2838; color: #c7d5e0; max-width: 680px; margin: 40px auto; padding: 24px; line-height: 1.5; }
  .card { background: #171a21; border: 1px solid #2a475e; border-radius: 8px; padding: 24px; box-shadow: 0 8px 24px rgba(0,0,0,0.4); }
  h1 { color: #66c0f4; margin-top: 0; }
  a { color: #66c0f4; }
  label { display: block; margin-top: 16px; font-weight: 600; color: #ffffff; }
  input[type=text], input[type=password] { width: 100%; box-sizing: border-box; padding: 10px; margin-top: 6px; border-radius: 4px; border: 1px solid #2a475e; background: #0e141b; color: #fff; font-family: monospace; }
  button { margin-top: 20px; background: linear-gradient(90deg, #06bfff 0%, #2d73ff 100%); color: #fff; border: none; padding: 12px 24px; font-size: 16px; font-weight: 600; border-radius: 4px; cursor: pointer; }
  button:hover { opacity: 0.92; }
  code { background: #0e141b; padding: 2px 6px; border-radius: 4px; color: #66c0f4; }
  ol { padding-left: 20px; }
</style>
</head>
<body>
<div class="card">
  <h1>go-steam-organizer Login</h1>
  <p>Sign in to Steam in your browser and provide your session cookie below:</p>
  <ol>
    <li>Open <a href="https://store.steampowered.com/login/" target="_blank" rel="noopener">store.steampowered.com/login</a> in a new tab and sign in.</li>
    <li>Press <code>F12</code> (DevTools) &rarr; <b>Application</b> (or <b>Storage</b>) &rarr; <b>Cookies</b> &rarr; <code>https://store.steampowered.com</code>.</li>
    <li>Copy the value of <code>steamLoginSecure</code> and paste it below.</li>
  </ol>
  <form method="POST" action="/">
    <label for="steamLoginSecure">steamLoginSecure Cookie (Required)</label>
    <input type="password" id="steamLoginSecure" name="steamLoginSecure" placeholder="7656119...%7C%7CeyJ..." required />

    <label for="sessionid">sessionid Cookie (Optional)</label>
    <input type="text" id="sessionid" name="sessionid" placeholder="Optional sessionid" />

    <label for="apiKey">Steam Web API Key (Optional, from steamcommunity.com/dev/apikey)</label>
    <input type="password" id="apiKey" name="apiKey" placeholder="Optional 32-char hex API key" />

    <button type="submit">Connect Steam Account</button>
  </form>
</div>
</body>
</html>`

