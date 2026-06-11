package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func RunApp(dbPath, model string) {
	// Check if backend is already running
	backendRunning := false
	if resp, err := http.Get("http://127.0.0.1:8765/api/health"); err == nil {
		resp.Body.Close()
		backendRunning = true
		fmt.Println("[INIT] Backend already running")
	}

	// Start backend as detached process if not running
	if !backendRunning {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "server")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// Force the backend to listen on 8765 so the GUI knows where to find it
		cmd.Env = append(os.Environ(), "FOTORO_ADDR=127.0.0.1:8765")
		// Detach: put in new process group so terminal Ctrl+C doesn't kill it
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
			Pgid:    0,
		}
		if err := cmd.Start(); err != nil {
			fmt.Printf("Failed to start backend: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[INIT] Backend started on 127.0.0.1:8765")

		// Wait for health
		for i := 0; i < 50; i++ {
			if resp, err := http.Get("http://127.0.0.1:8765/api/health"); err == nil {
				resp.Body.Close()
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Find Python GUI script
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	script := filepath.Join(dir, "fotoro-gui.py")
	if _, err := os.Stat(script); os.IsNotExist(err) {
		script = "fotoro-gui.py"
	}
	if _, err := os.Stat(script); os.IsNotExist(err) {
		fmt.Println("fotoro-gui.py not found. Server is running at http://127.0.0.1:8765")
		select {}
	}

	// Suppress MESA Vulkan warnings and launch GUI in its own process group
	fmt.Println("[INIT] Launching GUI...")
	cmd := exec.Command("python3", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "MESA_DEBUG=0", "MESA_VK_WSI_PRESENT_MODE=fifo")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Run(); err != nil {
		fmt.Printf("GUI exited: %v\n", err)
	}

	// Backend keeps running because it was detached. We exit here.
	fmt.Println("[INIT] GUI closed. Backend still running on 127.0.0.1:8765")
}
