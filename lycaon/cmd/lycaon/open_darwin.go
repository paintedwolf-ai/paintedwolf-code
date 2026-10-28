package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/lycaon/lycaon/internal/version"
)

// startApp asks Launch Services for the registered bundle; the app process belongs
// to Launch Services, so there is nothing here to stop or observe.
func startApp(ctx context.Context) (appStart, error) {
	out, err := exec.CommandContext(ctx, "open", "-b", version.BundleID).CombinedOutput() //nolint:gosec // G204 — fixed program and a compile-time constant bundle identifier
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("could not start Painted Wolf Code: %s", detail)
	}
	return unownedStart{}, nil
}

type unownedStart struct{}

func (unownedStart) Exited() <-chan error { return nil }
func (unownedStart) Stop()                {}
