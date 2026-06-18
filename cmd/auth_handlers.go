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
	LoadDotEnv()
	state := generateAuthState()
	origin := requestOrigin(r)
	cli := r.URL.Query().Get("cli") == "1"
	http.Redirect(w, r, buildWebLoginURL(origin+"/auth/callback", state, cli), http.StatusTemporaryRedirect)
}

func (s *Server) handleAuthCallbackPage(w http.ResponseWriter, r *http.Request) {
	LoadDotEnv()
	q := r.URL.Query()
	cliMode := q.Get("cli") == "1"

	if access := q.Get("access_token"); access != "" {
		state := q.Get("state")
		if !validateAuthState(state) {
			http.Error(w, "Invalid or expired sign-in session. Close this tab and run ./fotoro login again.", http.StatusBadRequest)
			return
		}
		clearAuthState(state)
		s.saveAuthFromCallback(
			access,
			q.Get("refresh_token"),
			q.Get("user_id"),
			q.Get("email"),
			q.Get("name"),
			q.Get("avatar_url"),
		)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(buildWebAuthSuccessHTML(cliMode)))
		return
	}

	// Fallback: Supabase OAuth code exchange (hash or ?code= from redirect)
	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseAnon := os.Getenv("SUPABASE_ANON_KEY")
	if supabaseURL == "" || supabaseAnon == "" {
		http.Error(w, "No session received. Your login page should redirect here with access_token and state query params.", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(buildAuthCallbackHTML(supabaseURL, supabaseAnon, cliMode)))
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
			s.loadStoredSession()
			s.authMu.RLock()
			user = s.authUser
			token = s.authAccessToken
			s.authMu.RUnlock()
		}
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
		if err := s.tailscale.Up(req.TailscaleAuthKey, []string{"tag:fotoro"}); err == nil {
			ip, _ := s.tailscale.GetTailscaleIP()
			tailnet, _ := s.tailscale.GetTailnetName()
			magicDNS, _ := s.tailscale.GetMagicDNS()
			s.db.UpdateServerConfig(map[string]interface{}{
				"tailscale_enabled": 1,
				"tailscale_ip":      ip,
				"tailnet_name":      tailnet,
				"tailnet_url":       magicDNS,
			})
			go s.syncNodeToCloud()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func requestOrigin(r *http.Request) string {
	host := r.Host
	if strings.HasPrefix(host, "localhost:") {
		host = "127.0.0.1" + strings.TrimPrefix(host, "localhost")
	}
	return "http://" + host
}

func buildAuthCallbackHTML(supabaseURL, supabaseAnon string, cliMode bool) string {
	doneMsg := "Signed in. You can close this tab and return to Fotoro."
	if cliMode {
		doneMsg = "Signed in. Close this tab and return to your terminal."
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Fotoro — Signed in</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
    background: #0a0a0a; color: #f5f5f5; min-height: 100vh;
    display: flex; align-items: center; justify-content: center; padding: 24px;
  }
  .card {
    width: min(400px, 100%%); background: #111; border: 1px solid #2a2a2a;
    border-radius: 20px; padding: 40px 32px; text-align: center;
  }
  .spinner {
    width: 32px; height: 32px; margin: 0 auto 20px;
    border: 3px solid #333; border-top-color: #fff;
    border-radius: 50%%; animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  h1 { font-size: 1.2rem; font-weight: 600; margin-bottom: 10px; }
  p { color: #888; font-size: 0.9rem; line-height: 1.5; }
  p.ok { color: #ccc; }
  p.err { color: #f87171; }
</style>
</head>
<body>
<div class="card">
  <div class="spinner" id="spin"></div>
  <h1>Completing sign-in…</h1>
  <p id="msg">Please wait.</p>
</div>
<script type="module">
import { createClient } from 'https://cdn.jsdelivr.net/npm/@supabase/supabase-js@2/+esm';

const sb = createClient(%q, %q);
const msg = document.getElementById('msg');
const spin = document.getElementById('spin');

async function saveSession(session) {
  const user = session.user || {};
  const meta = user.user_metadata || {};
  const res = await fetch('/api/auth/session', {
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
  if (!res.ok) throw new Error('Could not save session to Fotoro');
}

async function finish() {
  try {
    const hash = new URLSearchParams(window.location.hash.slice(1));
    const access = hash.get('access_token');
    const refresh = hash.get('refresh_token');
    const code = new URLSearchParams(window.location.search).get('code');
    if (code) {
      const { data, error } = await sb.auth.exchangeCodeForSession(code);
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
      throw new Error('No session found in URL — try signing in again');
    }
    msg.textContent = %q;
    msg.className = 'ok';
    spin.style.display = 'none';
  } catch (e) {
    msg.textContent = e?.message || String(e) || 'Sign-in failed';
    msg.className = 'err';
    spin.style.display = 'none';
  }
}

finish();
</script>
</body>
</html>`, supabaseURL, supabaseAnon, doneMsg)
}

func authConfigured() bool {
	return strings.TrimSpace(os.Getenv("SUPABASE_URL")) != "" &&
		strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY")) != ""
}

func (s *Server) authConfigured() bool {
	return authConfigured()
}
