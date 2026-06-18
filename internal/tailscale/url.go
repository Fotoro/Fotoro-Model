package tailscale

import "strings"

// PublicURL returns an HTTPS URL safe for browsers (encodes @ in email tailnets).
func PublicURL(dns string) string {
	if dns == "" {
		return ""
	}
	host := strings.ReplaceAll(dns, "@", "%40")
	return "https://" + host
}

// NormalizeServerURL fixes stored URLs that contain raw @ in the host.
func NormalizeServerURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	// https://host/path → encode @ only in host
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		slash := strings.IndexAny(rest, "/?#")
		host := rest
		suffix := ""
		if slash >= 0 {
			host = rest[:slash]
			suffix = rest[slash:]
		}
		host = strings.ReplaceAll(host, "@", "%40")
		return raw[:i+3] + host + suffix
	}
	return strings.ReplaceAll(raw, "@", "%40")
}
