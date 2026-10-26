package tailscale

import "testing"

func TestFunnelStatusIgnoresServeAndDisabledEntries(t *testing.T) {
	serve := map[string]interface{}{
		"TCP":         map[string]interface{}{"443": map[string]interface{}{"HTTPS": true}},
		"AllowFunnel": map[string]interface{}{"host:443": false},
	}
	if hasFunnelEnabled(serve) {
		t.Fatal("private Serve was reported as public Funnel")
	}
	serve["AllowFunnel"].(map[string]interface{})["host:443"] = true
	if !hasFunnelEnabled(serve) {
		t.Fatal("active Funnel was not detected")
	}
}
