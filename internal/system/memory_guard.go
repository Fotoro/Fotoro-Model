package system

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// MemoryStatus represents current memory state
type MemoryStatus struct {
	TotalMB      int64
	AvailableMB  int64
	UsedMB       int64
	SwapTotalMB  int64
	SwapUsedMB   int64
	SwapFreeMB   int64
}

// GetMemoryStatus reads current memory info from /proc/meminfo
func GetMemoryStatus() (*MemoryStatus, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("read meminfo: %w", err)
	}

	m := &MemoryStatus{}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, _ := strconv.ParseInt(fields[1], 10, 64)
		val = val / 1024 // convert KB to MB

		switch key {
		case "MemTotal":
			m.TotalMB = val
		case "MemAvailable":
			m.AvailableMB = val
		case "SwapTotal":
			m.SwapTotalMB = val
		case "SwapFree":
			m.SwapFreeMB = val
		}
	}

	m.UsedMB = m.TotalMB - m.AvailableMB
	m.SwapUsedMB = m.SwapTotalMB - m.SwapFreeMB
	return m, nil
}

// CanStartLLM checks if there's enough memory to safely start llama-server
func CanStartLLM(modelPath string) (bool, string) {
	mem, err := GetMemoryStatus()
	if err != nil {
		return false, fmt.Sprintf("Cannot read memory: %v", err)
	}

	// Estimate model size
	modelSizeMB := estimateModelSizeMB(modelPath)
	
	// Need: model_size * 1.3 (for overhead) + 2GB buffer
	requiredMB := int64(float64(modelSizeMB) * 1.3) + 2048

	// Check available RAM
	if mem.AvailableMB < requiredMB {
		// Check if swap can cover
		if mem.SwapFreeMB < (requiredMB - mem.AvailableMB) {
			return false, fmt.Sprintf(
				"Insufficient memory: need ~%d MB, have %d MB available, %d MB swap free. "+
				"Close other applications or use a smaller model.",
				requiredMB, mem.AvailableMB, mem.SwapFreeMB,
			)
		}
	}

	// Check swap usage - if already swapping heavily, refuse
	if mem.SwapTotalMB > 0 {
		swapPercent := float64(mem.SwapUsedMB) / float64(mem.SwapTotalMB) * 100
		if swapPercent > 50 {
			return false, fmt.Sprintf(
				"Swap usage too high (%.1f%%). System may become unresponsive. "+
				"Free up memory before starting LLM.",
				swapPercent,
			)
		}
	}

	return true, fmt.Sprintf("OK: %d MB available, need ~%d MB", mem.AvailableMB, requiredMB)
}

// MonitorSwap starts a goroutine that monitors swap and kills llama if needed
func MonitorSwap(thresholdPercent float64, killFunc func()) {
	go func() {
		for {
			mem, err := GetMemoryStatus()
			if err != nil {
				continue
			}
			if mem.SwapTotalMB > 0 {
				swapPercent := float64(mem.SwapUsedMB) / float64(mem.SwapTotalMB) * 100
				if swapPercent > thresholdPercent {
					fmt.Printf("[MEMORY GUARD] Swap at %.1f%% (threshold %.1f%%). Killing LLM...\\n", 
						swapPercent, thresholdPercent)
					killFunc()
					return
				}
			}
			// Check every 5 seconds
			time.Sleep(5 * time.Second)
		}
	}()
}

func estimateModelSizeMB(modelPath string) int64 {
	info, err := os.Stat(modelPath)
	if err != nil {
		// Default estimate for Qwen2.5-VL-3B Q4_K_M
		return 2200
	}
	return info.Size() / 1024 / 1024
}

// GetMemoryString returns human-readable memory status
func (m *MemoryStatus) String() string {
	swapPercent := 0.0
	if m.SwapTotalMB > 0 {
		swapPercent = float64(m.SwapUsedMB) / float64(m.SwapTotalMB) * 100
	}
	return fmt.Sprintf("RAM: %d/%d MB used (%.1f%%), Swap: %d/%d MB used (%.1f%%)",
		m.UsedMB, m.TotalMB, float64(m.UsedMB)/float64(m.TotalMB)*100,
		m.SwapUsedMB, m.SwapTotalMB, swapPercent,
	)
}
