package extpacks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestExtpacksInstallDoesNotRunHooks(t *testing.T) {
	origin := t.TempDir()
	gittest.Init(t, origin)
	hookDir := filepath.Join(origin, ".git", "hooks")
	testutil.FailErr(t, "mkdir hooks", os.MkdirAll(hookDir, 0o755))
	hook := filepath.Join(hookDir, "post-checkout")
	sentinel := filepath.Join(t.TempDir(), "post-checkout-ran")
	script := "#!/bin/sh\necho ran > " + sentinel + "\n"
	testutil.FailErr(t, "write hook", os.WriteFile(hook, []byte(script), 0o755))
	testutil.FailErr(t, "write readme", os.WriteFile(filepath.Join(origin, "README.md"), []byte("pack\n"), 0o644))
	gittest.CommitAll(t, origin, "init")

	dest := filepath.Join(t.TempDir(), "clone")
	if err := gitClone(context.Background(), origin, dest, "HEAD"); err != nil {
		t.Fatalf("gitClone: %v", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("post-checkout hook ran during pack clone")
	}
}
