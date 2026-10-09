package processes

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/userpath"
)

// wireUserPath establishes the shared PATH before executable discovery and child launches.
func (b *Runtime) ResolvePath(ctx context.Context) error {
	cfg, err := userpath.LoadConfig()
	if err != nil {
		// The embedded catalog is required for PATH resolution.
		return fmt.Errorf("user path catalog: %w", err)
	}
	var snapshot userpath.Snapshot
	if value, configured := os.LookupEnv("LYCAON_COMMAND_PATH"); configured {
		snapshot, err = userpath.Configured(value, cfg.Probe.MaxEntries)
		if err != nil {
			return fmt.Errorf("configured command path: %w", err)
		}
	} else {
		// A shutdown during startup cancels the probe instead of waiting out its timeout.
		snapshot = userpath.NewProvider(cfg).Resolve(ctx)
	}

	b.Path = snapshot
	exec.SetResolvedPathSource(snapshot.Value)

	attrs := []any{
		"source", string(snapshot.Source()),
		"entries", len(snapshot.Entries()),
	}
	if reason := snapshot.Reason(); reason != "" {
		// Record why shell PATH resolution fell back.
		attrs = append(attrs, "reason", reason)
		slog.Warn("resolved user PATH from fallback", attrs...)
		return nil
	}
	slog.Info("resolved user PATH", attrs...)
	return nil
}
