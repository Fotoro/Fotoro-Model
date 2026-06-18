// Add to fotoro.vercel.app after successful sign-in (login callback or dashboard).
// Reads ?state= from the login URL (store in sessionStorage on /login load).

/*
// On /login page load:
const params = new URLSearchParams(window.location.search);
if (params.get('state')) {
  sessionStorage.setItem('fotoro_cli_state', params.get('state'));
  sessionStorage.setItem('fotoro_redirect_uri', params.get('redirect_uri') || '');
  sessionStorage.setItem('fotoro_cli', params.get('cli') || '');
}

// After sign-in (dashboard layout or auth callback):
async function completeCliHandoff(supabase) {
  const state = sessionStorage.getItem('fotoro_cli_state');
  if (!state) return;

  const { data: { session } } = await supabase.auth.getSession();
  if (!session) return;

  const redirectUri = sessionStorage.getItem('fotoro_redirect_uri');
  const meta = session.user.user_metadata || {};

  // Option A — redirect back to local CLI (preferred when redirect_uri is set)
  if (redirectUri) {
    const u = new URL(redirectUri);
    u.searchParams.set('access_token', session.access_token);
    u.searchParams.set('refresh_token', session.refresh_token);
    u.searchParams.set('state', state);
    u.searchParams.set('user_id', session.user.id);
    u.searchParams.set('email', session.user.email || '');
    u.searchParams.set('name', meta.full_name || meta.name || '');
    u.searchParams.set('avatar_url', meta.avatar_url || meta.picture || '');
    if (sessionStorage.getItem('fotoro_cli') === '1') u.searchParams.set('cli', '1');
    sessionStorage.removeItem('fotoro_cli_state');
    sessionStorage.removeItem('fotoro_redirect_uri');
    sessionStorage.removeItem('fotoro_cli');
    window.location.href = u.toString();
    return;
  }

  // Option B — write tokens to Supabase for CLI polling
  await supabase.from('cli_auth_sessions').update({
    access_token: session.access_token,
    refresh_token: session.refresh_token,
    user_id: session.user.id,
    email: session.user.email,
    name: meta.full_name || meta.name || session.user.email,
    avatar_url: meta.avatar_url || meta.picture || '',
    completed_at: new Date().toISOString(),
  }).eq('state', state);

  sessionStorage.removeItem('fotoro_cli_state');
}
*/
