/**
 * BUG: "Loading Google sign-in…" spins forever on /login?cli=1
 *
 * Cause (google-one-tap.tsx): initialize() returns true when
 * localStorage.getItem("fotoro_access_token") exists, but never sets
 * ready=true — so the spinner never hides and handoff never runs.
 *
 * Also: tryHandoffFromStoredSession() returns "missing_refresh" when refresh
 * token is absent, then falls through to GoogleOneTap which hits the bug above.
 *
 * Apply these fixes in your site repo, then redeploy to Vercel.
 */

// ── 1. login-form.tsx — handle existing session in CLI mode ─────────────────

/*
useEffect(() => {
  captureCliParams(searchParams);

  async function boot() {
    const cli = getCliParams();
    const token = getAccessToken();
    const user = getStoredUser();

    if (cli && token && user) {
      // Already signed in — hand off immediately (don't wait for Google button)
      setCliPoll(true); // or setHandoffPending
      try {
        const result = await completeCliHandoff({
          access_token: token,
          refresh_token: getRefreshToken() ?? "",
          user_id: user.id,
          email: user.email ?? "",
          name: user.name ?? user.email ?? "",
        });
        if (result === "redirect") setRedirecting(true);
        if (result === "poll") setCliPoll(true);
      } catch (e) {
        setError(e.message);
        setBooting(false);
      }
      return;
    }

    if (!token) {
      setBooting(false);
      return;
    }

    const result = await tryHandoffFromStoredSession();
    if (result === "redirect") setRedirecting(true);
    else if (result === "poll") setCliPoll(true);
    else if (result === "missing_refresh" && cli) {
      // Access token only — still hand off for CLI
      await completeCliHandoff({
        access_token: token,
        refresh_token: "",
        user_id: user!.id,
        email: user!.email ?? "",
        name: user!.name ?? "",
      });
      setCliPoll(true);
      return;
    }
    setBooting(false);
  }

  void boot();
}, [searchParams, router, callbackUrl]);
*/

// ── 2. google-one-tap.tsx — fix initialize early-return ───────────────────

/*
function tryInitGoogle(): boolean {
  if (!clientId || !buttonRef.current || !window.google?.accounts?.id) {
    return false;
  }

  const existingToken = localStorage.getItem("fotoro_access_token");
  if (existingToken) {
    // Do NOT return true without setting ready — that causes infinite loading
    setReady(true);
    return true;
  }

  window.google.accounts.id.initialize({ client_id: clientId, callback: onCredential, ... });
  window.google.accounts.id.renderButton(buttonRef.current, { ... });
  window.google.accounts.id.prompt();
  setReady(true);
  return true;
}
*/

// ── 3. Prefer Supabase OAuth button (works on fotoro.vercel.app, no GSI origin issues) ─

/*
async function signInWithGoogleOAuth() {
  const supabase = createClient();
  const cli = getCliParams();
  const redirectTo = cli?.redirectUri
    ? `${window.location.origin}/auth/callback?cli=1&state=${cli.state}`
    : `${window.location.origin}/auth/callback`;

  const { data, error } = await supabase.auth.signInWithOAuth({
    provider: "google",
    options: { redirectTo },
  });
  if (error) throw error;
  if (data.url) window.location.href = data.url;
}
*/

// ── 4. Honor ?reauth=1 from CLI — clear stale local session ────────────────

/*
useEffect(() => {
  if (searchParams.get("reauth") === "1") {
    clearStoredSession(); // remove fotoro_access_token / refresh / user
  }
}, [searchParams]);
*/
