package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"fotoro/internal/auth"
	"fotoro/internal/db"
)

func (s *Server) loadStoredSession() {
	sess, err := s.db.GetAuthSession()
	if err != nil || sess == nil {
		return
	}
	s.authMu.Lock()
	s.authAccessToken = sess.AccessToken
	s.authRefreshToken = sess.RefreshToken
	s.authUser = &auth.User{
		ID:     sess.UserID,
		Email:  sess.Email,
		Name:   sess.Name,
		Avatar: sess.AvatarURL,
	}
	s.authMu.Unlock()
}

func (s *Server) handleAuthSetupPage(w http.ResponseWriter, r *http.Request) {
	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseAnon := os.Getenv("SUPABASE_ANON_KEY")
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	if supabaseURL == "" || supabaseAnon == "" {
		http.Error(w, "Supabase is not configured. Set SUPABASE_URL and SUPABASE_ANON_KEY.", http.StatusServiceUnavailable)
		return
	}
	origin := fmt.Sprintf("http://%s", r.Host)
	html := authSetupHTML(supabaseURL, supabaseAnon, googleClientID, origin)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

func (s *Server) handleAuthCallbackPage(w http.ResponseWriter, r *http.Request) {
	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseAnon := os.Getenv("SUPABASE_ANON_KEY")
	if supabaseURL == "" || supabaseAnon == "" {
		http.Error(w, "Supabase is not configured. Set SUPABASE_URL and SUPABASE_ANON_KEY.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(buildAuthCallbackHTML(supabaseURL, supabaseAnon)))
}

func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		s.authMu.RLock()
		user := s.authUser
		token := s.authAccessToken
		s.authMu.RUnlock()
		if user == nil || token == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"authenticated": false,
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": true,
			"access_token":  token,
			"user":          user,
		})
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
			return
		}
		var req struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			User         struct {
				ID     string `json:"id"`
				Email  string `json:"email"`
				Name   string `json:"name"`
				Avatar string `json:"avatar_url"`
			} `json:"user"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		if req.AccessToken == "" {
			http.Error(w, `{"error":"missing access_token"}`, http.StatusBadRequest)
			return
		}
		user := &auth.User{
			ID:       req.User.ID,
			Email:    req.User.Email,
			Name:     req.User.Name,
			Avatar:   req.User.Avatar,
			Provider: "google",
		}
		if s.supabaseAuth != nil {
			if verified, err := s.supabaseAuth.VerifyToken(req.AccessToken); err == nil {
				user = verified
			}
		}
		s.authMu.Lock()
		s.authAccessToken = req.AccessToken
		s.authRefreshToken = req.RefreshToken
		s.authUser = user
		s.authMu.Unlock()
		_ = s.db.SaveAuthSession(db.AuthSession{
			AccessToken:  req.AccessToken,
			RefreshToken: req.RefreshToken,
			UserID:       user.ID,
			Email:        user.Email,
			Name:         user.Name,
			AvatarURL:    user.Avatar,
		})
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": true,
			"user":          user,
		})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	complete, err := s.db.IsSetupComplete()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	s.authMu.RLock()
	authenticated := s.authUser != nil && s.authAccessToken != ""
	user := s.authUser
	s.authMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"setup_complete": complete,
		"authenticated":  authenticated,
		"user":             user,
	})
}

func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	s.authMu.RLock()
	authenticated := s.authUser != nil && s.authAccessToken != ""
	s.authMu.RUnlock()
	if !authenticated {
		http.Error(w, `{"error":"sign in required"}`, http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		TailscaleAuthKey string `json:"tailscale_auth_key"`
		ScheduleTime     string `json:"schedule_time"`
		ScheduleDays     string `json:"schedule_days"`
	}
	_ = json.Unmarshal(body, &req)
	updates := map[string]interface{}{
		"setup_complete": 1,
		"setup_step":     "done",
	}
	if req.ScheduleTime != "" {
		updates["schedule_time"] = req.ScheduleTime
	}
	if req.ScheduleDays != "" {
		updates["schedule_days"] = req.ScheduleDays
	}
	if err := s.db.UpdateServerConfig(updates); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if req.TailscaleAuthKey != "" {
		_ = s.tailscale.Up(req.TailscaleAuthKey, []string{"tag:fotoro"})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func authSetupHTML(supabaseURL, supabaseAnon, googleClientID, origin string) string {
	oneTapBlock := ""
	if googleClientID != "" {
		oneTapBlock = fmt.Sprintf(`
<div id="g_id_onload"
  data-client_id="%s"
  data-callback="handleGoogleCredential"
  data-auto_prompt="true"
  data-context="signin"
  data-itp_support="true">
</div>
<div class="g_id_signin" data-type="standard" data-theme="filled_black" data-size="large" data-text="continue_with" data-shape="pill"></div>`,
			googleClientID)
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Fotoro — Sign in</title>
<script src="https://accounts.google.com/gsi/client" async defer></script>
<script src="https://cdn.jsdelivr.net/npm/@supabase/supabase-js@2"></script>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: system-ui, -apple-system, Segoe UI, sans-serif;
    background: #000; color: #f5f5f5; min-height: 100vh;
    display: flex; align-items: center; justify-content: center;
    background-image: radial-gradient(60%% 60%% at 50%% 0%%, hsl(0 0%% 100%% / 0.12), transparent 60%%);
  }
  .card {
    width: min(420px, 92vw); background: #0d0d0d; border: 1px solid #242424;
    border-radius: 16px; padding: 32px 28px; text-align: center;
    box-shadow: inset 0 1px 0 hsl(0 0%% 100%% / 0.04), 0 1px 0 hsl(0 0%% 0%% / 0.4);
  }
  h1 { font-size: 1.35rem; font-weight: 600; margin-bottom: 8px; letter-spacing: -0.02em; }
  p { color: #9e9e9e; font-size: 0.92rem; line-height: 1.5; margin-bottom: 24px; }
  .status { margin-top: 18px; font-size: 0.85rem; color: #9e9e9e; min-height: 1.2em; }
  .status.ok { color: #f5f5f5; }
  .status.err { color: #f5f5f5; }
  button.oauth {
    width: 100%%; height: 44px; border-radius: 8px; border: 1px solid #242424;
    background: #f5f5f5; color: #0f0f0f; font-weight: 600; font-size: 0.95rem;
    cursor: pointer; margin-top: 12px;
  }
  button.oauth:hover { background: #fff; }
  .gsi-wrap { display: flex; justify-content: center; margin-top: 8px; min-height: 44px; }
</style>
</head>
<body>
<div class="card">
  <h1>Sign in to Fotoro</h1>
  <p>Use your Google account to set up this installation. You only need to do this once.</p>
  <div class="gsi-wrap">%s</div>
  <button class="oauth" type="button" onclick="signInWithOAuth()">Continue with Google</button>
  <div id="status" class="status">Waiting for Google sign-in…</div>
</div>
<script>
const SUPABASE_URL = %q;
const SUPABASE_ANON = %q;
const REDIRECT = %q + '/auth/callback';
const supabase = window.supabase.createClient(SUPABASE_URL, SUPABASE_ANON);

function setStatus(msg, cls) {
  const el = document.getElementById('status');
  el.textContent = msg;
  el.className = 'status' + (cls ? ' ' + cls : '');
}

async function persistSession(session) {
  const user = session.user || {};
  const meta = user.user_metadata || {};
  const payload = {
    access_token: session.access_token,
    refresh_token: session.refresh_token,
    user: {
      id: user.id,
      email: user.email,
      name: meta.full_name || meta.name || user.email,
      avatar_url: meta.avatar_url || meta.picture || ''
    }
  };
  const res = await fetch('/api/auth/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  });
  if (!res.ok) throw new Error('Could not save session');
  setStatus('Signed in as ' + (user.email || 'your account') + '. You can return to Fotoro.', 'ok');
}

async function handleGoogleCredential(response) {
  try {
    setStatus('Signing in…');
    const { data, error } = await supabase.auth.signInWithIdToken({
      provider: 'google',
      token: response.credential
    });
    if (error) throw error;
    await persistSession(data.session);
  } catch (e) {
    setStatus(e.message || 'Sign-in failed', 'err');
  }
}

async function signInWithOAuth() {
  try {
    setStatus('Opening Google…');
    const { data, error } = await supabase.auth.signInWithOAuth({
      provider: 'google',
      options: { redirectTo: REDIRECT, skipBrowserRedirect: false }
    });
    if (error) throw error;
    if (data && data.url) window.location.href = data.url;
  } catch (e) {
    setStatus(e.message || 'OAuth failed', 'err');
  }
}
</script>
</body>
</html>`, oneTapBlock, supabaseURL, supabaseAnon, origin)
}

func buildAuthCallbackHTML(supabaseURL, supabaseAnon string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<title>Fotoro — Signed in</title>
<script src="https://cdn.jsdelivr.net/npm/@supabase/supabase-js@2"></script>
<style>
  body { font-family: system-ui, sans-serif; background:#000; color:#f5f5f5;
    min-height:100vh; display:flex; align-items:center; justify-content:center; }
  .card { background:#0d0d0d; border:1px solid #242424; border-radius:16px; padding:32px; text-align:center; max-width:420px; }
  p { color:#9e9e9e; margin-top:12px; }
</style>
</head>
<body>
<div class="card">
  <h1>Completing sign-in…</h1>
  <p id="msg">Please wait.</p>
</div>
<script>
const supabase = window.supabase.createClient(%q, %q);

async function saveSession(session) {
  const user = session.user || {};
  const meta = user.user_metadata || {};
  await fetch('/api/auth/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      access_token: session.access_token,
      refresh_token: session.refresh_token,
      user: {
        id: user.id,
        email: user.email,
        name: meta.full_name || meta.name || user.email,
        avatar_url: meta.avatar_url || meta.picture || ''
      }
    })
  });
}

async function finish() {
  const msg = document.getElementById('msg');
  try {
    const hash = new URLSearchParams(window.location.hash.slice(1));
    let access = hash.get('access_token');
    let refresh = hash.get('refresh_token');
    const query = new URLSearchParams(window.location.search);
    const code = query.get('code');
    if (code) {
      const { data, error } = await supabase.auth.exchangeCodeForSession(code);
      if (error) throw error;
      await saveSession(data.session);
    } else if (access) {
      await fetch('/api/auth/session', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          access_token: access,
          refresh_token: refresh || '',
          user: { email: '', name: '' }
        })
      });
    } else {
      msg.textContent = 'No session found. Close this tab and try again.';
      return;
    }
    msg.textContent = 'Signed in. You can close this tab and return to Fotoro.';
  } catch (e) {
    msg.textContent = e.message || 'Sign-in failed';
  }
}
finish();
</script>
</body>
</html>`, supabaseURL, supabaseAnon)
}

func (s *Server) authConfigured() bool {
	return strings.TrimSpace(os.Getenv("SUPABASE_URL")) != "" &&
		strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY")) != ""
}
