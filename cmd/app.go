package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"fotoro/internal/system"
)

func RunApp(dbPath, model string) {
	// Check if backend already running
	backendRunning := false
	if resp, err := http.Get("http://127.0.0.1:8765/api/health"); err == nil {
		resp.Body.Close()
		backendRunning = true
		fmt.Println("[INIT] Backend already running")
	}

	// Start backend as detached process if not running
	if !backendRunning {
		modelPath := os.Getenv("FOTORO_MODEL_PATH")
		if modelPath == "" {
			modelPath = "./models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf"
		}
		if ok, msg := system.CanStartLLM(modelPath); !ok {
			fmt.Printf("[ERROR] Cannot start: %s\\n", msg)
			fmt.Println("[HINT] Free up memory or run 'fotoro server' without LLM features")
			os.Exit(1)
		}

		exe, _ := os.Executable()
		cmd := exec.Command(exe, "server")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), "FOTORO_ADDR=127.0.0.1:8765")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
			Pgid:    0,
		}
		if err := cmd.Start(); err != nil {
			fmt.Printf("Failed to start backend: %v\\n", err)
			os.Exit(1)
		}
		fmt.Println("[INIT] Backend started on 127.0.0.1:8765")

		for i := 0; i < 50; i++ {
			if resp, err := http.Get("http://127.0.0.1:8765/api/health"); err == nil {
				resp.Body.Close()
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Find and launch Qt6 GUI
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)

	guiPaths := []string{
		filepath.Join(dir, "fotoro-gui"),
		filepath.Join(dir, "fotoro-desktop"),
		"./fotoro-gui",
		"./fotoro-desktop",
	}

	var guiPath string
	for _, p := range guiPaths {
		if _, err := os.Stat(p); err == nil {
			guiPath = p
			break
		}
	}

	if guiPath == "" {
		fmt.Println("[WARN] GUI binary not found. Building from source...")
		buildCmd := exec.Command("go", "build", "-o", "fotoro-gui", "./gui")
		buildCmd.Dir = filepath.Dir(exe)
		if err := buildCmd.Run(); err != nil {
			fmt.Println("[WARN] Could not build GUI. Server is running at http://127.0.0.1:8765")
			fmt.Println("[INFO] Access the web interface or use API directly")
			select {}
		}
		guiPath = "./fotoro-gui"
	}

	fmt.Println("[INIT] Launching GUI...")
	guiCmd := exec.Command(guiPath)
	guiCmd.Stdout = os.Stdout
	guiCmd.Stderr = os.Stderr
	guiCmd.Env = append(os.Environ(),
		"QT_QPA_PLATFORM=xcb",
		"MESA_DEBUG=0",
	)
	guiCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := guiCmd.Run(); err != nil {
		fmt.Printf("GUI exited: %v\\n", err)
	}

	fmt.Println("[INIT] GUI closed. Backend still running on 127.0.0.1:8765")
	fmt.Println("[INFO] Stop with: kill $(lsof -t -i:8765)")
}
