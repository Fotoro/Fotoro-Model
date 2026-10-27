package cmd

import (
	"context"
	"time"
)

func (s *Server) startDerivativeWorker() {
	_ = s.media.ResetInterruptedDerivativeJobs()
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.backgroundActive.Add(1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			_, _ = s.media.ProcessDerivativeJobs(ctx, 2)
			cancel()
			s.backgroundActive.Add(-1)
		}
	}()
}
