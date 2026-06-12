package system

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

type Specs struct {
	CPUCores      int
	PhysicalCores int
	RAMBytes      uint64
	RAMGB         int
	HasGPU        bool
	GPUName       string
	OS            string
	Arch          string
}

// Detect gathers system specs
func Detect() (*Specs, error) {
	s := &Specs{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}

	// CPU
	s.CPUCores = runtime.NumCPU()
	s.PhysicalCores = detectPhysicalCores()

	// RAM
	var err error
	s.RAMBytes, err = detectRAM()
	if err != nil {
		return nil, err
	}
	s.RAMGB = int(s.RAMBytes / 1024 / 1024 / 1024)

	// GPU
	s.HasGPU, s.GPUName = detectGPU()

	return s, nil
}

func detectPhysicalCores() int {
	// Try lscpu first
	if out, err := exec.Command("lscpu", "-p").Output(); err == nil {
		cores := make(map[string]struct{})
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			parts := strings.Split(line, ",")
			if len(parts) > 1 {
				cores[parts[1]] = struct{}{} // core id
			}
		}
		if len(cores) > 0 {
			return len(cores)
		}
	}
	return runtime.NumCPU() / 2 // hyperthreading guess
}

func detectRAM() (uint64, error) {
	if runtime.GOOS == "linux" {
		f, err := os.Open("/proc/meminfo")
		if err != nil {
			return 0, err
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseUint(fields[1], 10, 64)
					return kb * 1024, nil
				}
			}
		}
	}
	// Fallback
	return 8 * 1024 * 1024 * 1024, nil // assume 8GB
}

func detectGPU() (bool, string) {
	// Check nvidia-smi
	if out, err := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output(); err == nil {
		name := strings.TrimSpace(string(out))
		if name != "" {
			return true, name
		}
	}
	// Check rocm
	if out, err := exec.Command("rocm-smi", "--showproductname").Output(); err == nil {
		if strings.Contains(string(out), "GPU") {
			return true, "AMD GPU"
		}
	}
	// Check Apple Silicon
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output(); err == nil {
			if strings.Contains(string(out), "Apple") {
				return true, "Apple Silicon GPU"
			}
		}
	}
	return false, ""
}

// RecommendConfig returns env vars based on specs
func (s *Specs) RecommendConfig() map[string]string {
	cfg := make(map[string]string)

	// Threads: physical cores, not logical
	threads := s.PhysicalCores
	if threads < 1 {
		threads = 2
	}
	cfg["LLAMA_THREADS"] = strconv.Itoa(threads)

	// Context size and image tokens based on RAM
	switch {
	case s.RAMGB >= 32:
		cfg["LLAMA_CTX_SIZE"] = "2048"
		cfg["IMAGE_MAX_TOKENS"] = "512"
		cfg["VLM_SIZE"] = "448"
	case s.RAMGB >= 16:
		cfg["LLAMA_CTX_SIZE"] = "1024"
		cfg["IMAGE_MAX_TOKENS"] = "256"
		cfg["VLM_SIZE"] = "448"
	case s.RAMGB >= 8:
		cfg["LLAMA_CTX_SIZE"] = "512"
		cfg["IMAGE_MAX_TOKENS"] = "128"
		cfg["VLM_SIZE"] = "336"
	default:
		cfg["LLAMA_CTX_SIZE"] = "512"
		cfg["IMAGE_MAX_TOKENS"] = "64"
		cfg["VLM_SIZE"] = "224"
	}

	// Batch sizes: smaller on low-RAM or CPU-only
	if s.RAMGB < 8 || !s.HasGPU {
		cfg["LLAMA_BATCH_SIZE"] = "256"
		cfg["LLAMA_UBATCH_SIZE"] = "256"
	} else {
		cfg["LLAMA_BATCH_SIZE"] = "512"
		cfg["LLAMA_UBATCH_SIZE"] = "512"
	}

	// GPU offload
	if s.HasGPU {
		cfg["LLAMA_NGL"] = "99" // offload all layers
	} else {
		cfg["LLAMA_NGL"] = "0"
	}

	// Fotoro-specific
	cfg["FOTORO_VISION_WORKERS"] = strconv.Itoa(min(4, threads))

	return cfg
}

func (s *Specs) String() string {
	gpu := "None"
	if s.HasGPU {
		gpu = s.GPUName
	}
	return fmt.Sprintf("OS: %s/%s | Cores: %d phys / %d logical | RAM: %d GB | GPU: %s",
		s.OS, s.Arch, s.PhysicalCores, s.CPUCores, s.RAMGB, gpu)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
