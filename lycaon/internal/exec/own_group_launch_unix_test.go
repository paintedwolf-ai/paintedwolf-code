//go:build unix

package exec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunInOwnGroupReportsACommandRemovedAfterPreparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "removed-command")
	testutil.FailErr(t, "create executable", os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700))
	cmd, cleanup, err := PrepareCommand(t.Context(), path, nil, ExecOpts{Launch: HostLaunch("exec test")})
	testutil.FailErr(t, "prepare executable", err)
	defer cleanup()
	testutil.FailErr(t, "remove prepared executable", os.Remove(path))
	if err := RunInOwnGroup(cmd); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed executable launch = %v", err)
	}
}

func TestHelperProbeGroupSharesSupervisedSettlement(t *testing.T) {
	if _, err := StartInOwnGroup(exec.Command("/bin/true")); err == nil {
		t.Fatal("public group accepted an unprepared command")
	}
	testutil.FailErr(t, "settle fixed helper probe", runHelperProbeGroup(exec.Command("/bin/true")))
	if err := runHelperProbeGroup(exec.Command("/nonexistent/probe")); err == nil {
		t.Fatal("missing helper probe succeeded")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := exec.CommandContext(ctx, "/bin/true")
	if err := runHelperProbeGroup(cmd); !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("cancelled probe started: process=%v err=%v", cmd.Process, err)
	}
}
