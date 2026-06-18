package tailscale

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Install runs scripts/tailscale-install.sh (bundled next to the fotoro binary or cwd).
func (m *Manager) Install() error {
	script, err := locateInstallScript()
	if err != nil {
		return err
	}

	fmt.Printf("[TAILSCALE] Running install script: %s\n", script)
	cmd := exec.Command("bash", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tailscale install script failed: %w", err)
	}
	return nil
}

func locateInstallScript() (string, error) {
	candidates := []string{
		"scripts/tailscale-install.sh",
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "scripts/tailscale-install.sh"),
			filepath.Join(dir, "..", "scripts/tailscale-install.sh"),
		)
	}

	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("scripts/tailscale-install.sh not found — run from FotoroModel root or place script beside binary")
}
