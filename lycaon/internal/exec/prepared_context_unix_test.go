//go:build darwin || linux

package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPreparedLaunchRequiresBothCaptureAndStartContexts(t *testing.T) {
	for _, scope := range []string{"capture", "start"} {
		t.Run(scope, func(t *testing.T) {
			capture, cancelCapture := context.WithCancel(t.Context())
			defer cancelCapture()
			start, cancelStart := context.WithCancel(t.Context())
			defer cancelStart()
			root := t.TempDir()
			cmd, cleanup, err := PrepareCommand(capture, "/bin/sh", []string{"-c", "printf executed > effect"}, ExecOpts{Launch: HostLaunch("context cancellation fixture"), Dir: root})
			testutil.FailErr(t, "prepare actual process", err)
			defer cleanup()
			if scope == "capture" {
				cancelCapture()
			} else {
				cancelStart()
			}
			if err := StartPrepared(start, cmd); !errors.Is(err, context.Canceled) || cmd.Process != nil {
				t.Fatalf("canceled %s scope reached native start: process=%v error=%v", scope, cmd.Process, err)
			}
			if _, err := os.Stat(filepath.Join(root, "effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("canceled launch published an effect: %v", err)
			}
		})
	}
}
