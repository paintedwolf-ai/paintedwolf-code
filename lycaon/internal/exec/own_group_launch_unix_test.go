//go:build unix

package exec

import (
	"os"
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
	if err := RunInOwnGroup(cmd); !os.IsNotExist(err) {
		t.Fatalf("removed executable launch = %v", err)
	}
}
