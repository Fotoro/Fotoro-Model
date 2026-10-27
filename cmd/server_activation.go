package cmd

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"fotoro/internal/scheduler"
)

// systemd keeps the listening socket open while the API process is asleep.
// A request to that socket starts the user service and is delivered to Serve.
func serverListener(addr string) (net.Listener, bool, error) {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") == "1" {
		file := os.NewFile(3, "fotoro.socket")
		if file == nil {
			return nil, false, fmt.Errorf("socket activation fd is unavailable")
		}
		listener, err := net.FileListener(file)
		_ = file.Close()
		if err != nil {
			return nil, false, fmt.Errorf("socket activation: %w", err)
		}
		return listener, true, nil
	}
	listener, err := net.Listen("tcp", addr)
	return listener, false, err
}

func idleShutdown(activated bool, lastActivity *atomic.Int64, activeRequests *atomic.Int32, queue *scheduler.JobQueue, server *Server) <-chan struct{} {
	if !activated {
		return nil
	}
	done := make(chan struct{})
	idleFor := 10 * time.Minute
	if raw := os.Getenv("FOTORO_IDLE_MINUTES"); raw != "" {
		if minutes, err := strconv.Atoi(raw); err == nil && minutes > 0 {
			idleFor = time.Duration(minutes) * time.Minute
		}
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if activeRequests.Load() != 0 || time.Since(time.Unix(0, lastActivity.Load())) < idleFor {
				continue
			}
			if queue != nil && queue.IsProcessing() {
				continue
			}
			if server != nil && server.backgroundActive.Load() != 0 {
				continue
			}
			if server != nil && server.media != nil {
				status, err := server.media.BackupStatus()
				if err == nil && status.Available && status.Pending > 0 {
					continue
				}
			}
			close(done)
			return
		}
	}()
	return done
}
