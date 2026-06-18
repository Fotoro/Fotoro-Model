package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"fotoro/internal/auth"
	"fotoro/internal/cliauth"
	"fotoro/internal/db"
	"fotoro/internal/tailscale"
)

const defaultAuthURL = "https://fotoro.vercel.app/login"

var pendingAuthStates sync.Map // state -> expiry time.Time

type authSessionResponse struct {
	Authenticated bool       `json:"authenticated"`
	AccessToken   string     `json:"access_token"`
	User          *auth.User `json:"user"`
}

// RunWebSignIn opens the hosted Fotoro login page. After sign-in the website
// redirects to the local /auth/callback with tokens; the CLI polls until saved.
func RunWebSignIn(dbPath, addr string, timeout time.Duration) (string, *auth.User, error) {
	LoadDotEnv()

	stop, baseURL, err := ensureAuthServer(addr, dbPath)
	if err != nil {
		return "", nil, err
	}
	defer stop()

	state := generateAuthState()
	loginURL := buildWebLoginURL(baseURL+"/auth/callback", state, true)

	useHandoff := cliauth.IsConfigured() && cliauth.HasServiceKey()
	if useHandoff {
		if err := cliauth.CreateSession(state, timeout); err != nil {
			fmt.Printf("[WARN] CLI handoff unavailable (%v) — relying on redirect callback only\n", err)
			useHandoff = false
		}
	}

	fmt.Println("Opening Fotoro sign-in in your browser…")
	fmt.Printf("If it does not open, visit:\n%s\n\n", loginURL)
	if err := openBrowser(loginURL); err != nil {
		fmt.Printf("(Could not open browser automatically: %v)\n", err)
	}

	if useHandoff {
		fmt.Println("Waiting for sign-in on fotoro.vercel.app…")
		fmt.Println("When done, close the browser tab — this terminal continues automatically.")
	} else {
		fmt.Println("Waiting for sign-in… (browser will redirect back here when done)")
	}
	if !useHandoff {
		fmt.Println()
		fmt.Println("If the page shows “Loading Google sign-in…” forever, log out at fotoro.vercel.app and retry.")
		fmt.Println("See supabase/website_login_fix.js for the website patch.")
	}
	fmt.Println()
	token, user, err := pollUntilSignedIn(baseURL, dbPath, state, useHandoff, timeout)
	if err != nil {
		return "", nil, err
	}

	fmt.Printf("✅ Signed in as %s\n", user.Email)
	return token, user, nil
}

func webAuthURL() string {
	if u := strings.TrimSpace(os.Getenv("FOTORO_AUTH_URL")); u != "" {
		return u
	}
	return defaultAuthURL
}

func buildWebLoginURL(redirectURI, state string, cli bool) string {
	u, err := url.Parse(webAuthURL())
	if err != nil {
		u, _ = url.Parse(defaultAuthURL)
	}
	q := u.Query()
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	if cli {
		q.Set("cli", "1")
	}
	q.Set("reauth", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func generateAuthState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	pendingAuthStates.Store(state, time.Now().Add(10*time.Minute))
	return state
}

func validateAuthState(state string) bool {
	if state == "" {
		return false
	}
	v, ok := pendingAuthStates.Load(state)
	if !ok {
		return false
	}
	if time.Now().After(v.(time.Time)) {
		pendingAuthStates.Delete(state)
		return false
	}
	return true
}

func clearAuthState(state string) {
	if state != "" {
		pendingAuthStates.Delete(state)
	}
}

func (s *Server) handleAuthLoginURL(w http.ResponseWriter, r *http.Request) {
	LoadDotEnv()
	state := generateAuthState()
	origin := requestOrigin(r)
	cli := r.URL.Query().Get("cli") == "1"
	loginURL := buildWebLoginURL(origin+"/auth/callback", state, cli)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url":   loginURL,
		"state": state,
	})
}

func (s *Server) saveAuthFromCallback(access, refresh, userID, email, name, avatar string) *auth.User {
	user := &auth.User{
		ID:       userID,
		Email:    email,
		Name:     name,
		Avatar:   avatar,
		Provider: "google",
	}
	if s.supabaseAuth != nil && access != "" {
		if verified, err := s.supabaseAuth.VerifyToken(access); err == nil {
			user = verified
		}
	}
	s.authMu.Lock()
	s.authAccessToken = access
	s.authRefreshToken = refresh
	s.authUser = user
	s.authMu.Unlock()
	_ = s.db.SaveAuthSession(db.AuthSession{
		AccessToken:  access,
		RefreshToken: refresh,
		UserID:       user.ID,
		Email:        user.Email,
		Name:         user.Name,
		AvatarURL:    user.Avatar,
	})
	return user
}

func ensureAuthServer(addr, dbPath string) (stop func(), baseURL string, err error) {
	LoadDotEnv()

	baseURL = "http://" + addr
	if reachable(baseURL + "/api/health") {
		return func() {}, baseURL, nil
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, "", fmt.Errorf("open database: %w", err)
	}

	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseAnon := os.Getenv("SUPABASE_ANON_KEY")
	var supabaseAuth *auth.SupabaseAuth
	if supabaseURL != "" && supabaseAnon != "" {
		supabaseAuth = auth.NewSupabaseAuth(supabaseURL, supabaseAnon)
		_ = supabaseAuth.Initialize()
	}

	s := &Server{
		db:           database,
		supabaseAuth: supabaseAuth,
		tailscale:    tailscale.NewManager(),
	}
	s.loadStoredSession()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/session", s.handleAuthSession)
	mux.HandleFunc("/api/auth/login-url", s.handleAuthLoginURL)
	mux.HandleFunc("/auth/callback", s.handleAuthCallbackPage)

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "[AUTH] server error: %v\n", err)
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if reachable(baseURL + "/api/health") {
			return func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
				database.Close()
			}, baseURL, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	_ = srv.Shutdown(context.Background())
	database.Close()
	return nil, "", fmt.Errorf("auth server did not start on %s (port may be in use)", addr)
}

func pollUntilSignedIn(baseURL, dbPath, state string, useHandoff bool, timeout time.Duration) (string, *auth.User, error) {
	deadline := time.Now().Add(timeout)
	for {
		if token, user, ok, err := fetchAuthSession(baseURL); err != nil {
			return "", nil, err
		} else if ok {
			return token, user, nil
		}

		if useHandoff {
			if sess, ok, err := cliauth.TryFetchSession(state); err != nil {
				return "", nil, err
			} else if ok {
				user, err := persistCLISession(baseURL, dbPath, sess)
				if err != nil {
					return "", nil, err
				}
				return sess.AccessToken, user, nil
			}
		}

		if time.Now().After(deadline) {
			return "", nil, fmt.Errorf("sign-in timed out — complete sign-in on fotoro.vercel.app (ensure cli_auth_sessions has completed_at column; see supabase/cli_auth_sessions_add_completed_at.sql)")
		}
		time.Sleep(1200 * time.Millisecond)
	}
}

func persistCLISession(baseURL, dbPath string, sess *cliauth.Session) (*auth.User, error) {
	payload := map[string]interface{}{
		"access_token":  sess.AccessToken,
		"refresh_token": sess.RefreshToken,
		"user": map[string]string{
			"id":         sess.UserID,
			"email":      sess.Email,
			"name":       sess.Name,
			"avatar_url": sess.AvatarURL,
		},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(baseURL+"/api/auth/session", "application/json", strings.NewReader(string(body)))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var out authSessionResponse
			if json.NewDecoder(resp.Body).Decode(&out) == nil && out.User != nil {
				return out.User, nil
			}
		}
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer database.Close()

	user := &auth.User{
		ID:       sess.UserID,
		Email:    sess.Email,
		Name:     sess.Name,
		Avatar:   sess.AvatarURL,
		Provider: "google",
	}
	_ = database.SaveAuthSession(db.AuthSession{
		AccessToken:  sess.AccessToken,
		RefreshToken: sess.RefreshToken,
		UserID:       user.ID,
		Email:        user.Email,
		Name:         user.Name,
		AvatarURL:    user.Avatar,
	})
	return user, nil
}

func pollAuthSession(baseURL string, timeout time.Duration) (string, *auth.User, error) {
	return pollUntilSignedIn(baseURL, "", "", false, timeout)
}

func fetchAuthSession(baseURL string) (string, *auth.User, bool, error) {
	resp, err := http.Get(baseURL + "/api/auth/session")
	if err != nil {
		return "", nil, false, nil
	}
	defer resp.Body.Close()

	var out authSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil, false, nil
	}
	if !out.Authenticated || out.AccessToken == "" || out.User == nil {
		return "", nil, false, nil
	}
	return out.AccessToken, out.User, true, nil
}

func reachable(url string) bool {
	resp, err := http.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

func ensureLocalUser(database *db.DB, user *auth.User) (int64, error) {
	return database.GetOrCreateUser(user.Email, user.Name, "google:"+user.ID)
}

func buildWebAuthSuccessHTML(cliMode bool) string {
	msg := "Signed in. You can close this tab and return to Fotoro."
	if cliMode {
		msg = "Signed in. Close this tab and return to your terminal."
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"/><title>Fotoro — Signed in</title>
<style>
  body { font-family: system-ui, sans-serif; background:#0a0a0a; color:#f5f5f5;
    min-height:100vh; display:flex; align-items:center; justify-content:center; margin:0; }
  .card { background:#111; border:1px solid #2a2a2a; border-radius:20px; padding:40px 32px;
    text-align:center; max-width:400px; }
  h1 { font-size:1.2rem; margin-bottom:12px; }
  p { color:#888; line-height:1.5; }
</style></head><body><div class="card">
  <h1>✓ You're signed in</h1>
  <p>%s</p>
</div></body></html>`, msg)
}
