package tailscale

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Reset disconnects, logs out, stops the daemon, wipes identity, and removes packages.
func (m *Manager) Reset() error {
	if m.IsInstalled() {
		fmt.Println("[TAILSCALE] Logging out and disconnecting…")
		_ = m.Logout()
		_ = exec.Command("sudo", "systemctl", "stop", "tailscaled").Run()
		_ = exec.Command("sudo", "systemctl", "disable", "tailscaled").Run()
		fmt.Println("[TAILSCALE] Clearing saved machine identity…")
		_ = exec.Command("sudo", "rm", "-rf", "/var/lib/tailscale").Run()
	}

	if script, err := locateUninstallScript(); err == nil {
		fmt.Printf("[TAILSCALE] Running uninstall script: %s\n", script)
		cmd := exec.Command("bash", script)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("tailscale uninstall script failed: %w", err)
		}
		return nil
	}

	return uninstallViaPackageManager()
}

func locateUninstallScript() (string, error) {
	candidates := []string{"scripts/tailscale-uninstall.sh"}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "scripts/tailscale-uninstall.sh"),
			filepath.Join(dir, "..", "scripts/tailscale-uninstall.sh"),
		)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found")
}

func uninstallViaPackageManager() error {
	if !mInstalled() {
		fmt.Println("[TAILSCALE] Already removed.")
		return nil
	}

	fmt.Println("[TAILSCALE] Removing Tailscale package…")
	for _, args := range [][]string{
		{"dnf", "remove", "-y", "tailscale"},
		{"yum", "remove", "-y", "tailscale"},
		{"apt-get", "remove", "-y", "tailscale"},
		{"pacman", "-R", "--noconfirm", "tailscale"},
	} {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		cmd := exec.Command("sudo", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			fmt.Println("[TAILSCALE] Package removed.")
			return nil
		}
	}
	return fmt.Errorf("could not remove tailscale automatically — run: sudo dnf remove tailscale")
}

func mInstalled() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}
