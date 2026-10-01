package app

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// ParentPIDEnv names the process whose exit stops this engine.
const ParentPIDEnv = "LYCAON_PARENT_PID"

// parentPollInterval bounds release of a stranded store lock.
const parentPollInterval = 2 * time.Second

// watchParentExit reports a channel closed once the spawning process is gone.
func watchParentExit(ctx context.Context) <-chan struct{} {
	raw := os.Getenv(ParentPIDEnv)
	if raw == "" {
		return nil
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 1 {
		// PID 1 cannot be the desktop parent.
		slog.WarnContext(ctx, "ignoring unusable parent pid", "env", ParentPIDEnv, "value", raw)
		return nil
	}

	gone := make(chan struct{})
	go func() {
		defer close(gone)
		ticker := time.NewTicker(parentPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !osprocess.Alive(pid) {
					slog.InfoContext(ctx, "spawning process is gone — shutting down", "parent_pid", pid)
					return
				}
			}
		}
	}()
	return gone
}
