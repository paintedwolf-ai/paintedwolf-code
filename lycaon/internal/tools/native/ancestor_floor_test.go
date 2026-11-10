package native_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func TestNativeDeleteAndMoveProtectControlPlaneAncestorsAndAllowSiblings(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	dotConfig := filepath.Join(home, ".config")
	cfgDir := filepath.Join(dotConfig, "paintedwolf-state")
	siblingDir := filepath.Join(home, "work")
	for _, dir := range []string{cfgDir, siblingDir} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	stateFile := filepath.Join(cfgDir, "api.token")
	testutil.FailErr(t, "write state", os.WriteFile(stateFile, []byte("token"), 0o600))

	srcSibling := filepath.Join(siblingDir, "a.txt")
	dstSibling := filepath.Join(siblingDir, "b.txt")
	testutil.FailErr(t, "write sibling", os.WriteFile(srcSibling, []byte("hello"), 0o600))

	aliasToConfig := filepath.Join(home, "config-alias")
	testutil.FailErr(t, "symlink config", os.Symlink(dotConfig, aliasToConfig))

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	homeCtx := tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "home", Label: "home", Path: home, IsPrimary: true}},
			ActiveRootID: "home",
		},
	}

	delTool := &native.DeleteTool{}
	moveTool := &native.MoveTool{}

	// 1. Ordinary sibling move and delete under attached $HOME succeed.
	if _, err := moveTool.Move(t.Context(), native.MoveInput{
		Pairs: []native.FilePair{{From: "work/a.txt", To: "work/b.txt"}},
	}, homeCtx); err != nil {
		t.Fatalf("ordinary sibling Move under attached $HOME failed: %v", err)
	}
	if _, err := delTool.Delete(t.Context(), native.DeleteInput{
		Paths: []string{"work/b.txt"},
	}, homeCtx); err != nil {
		t.Fatalf("ordinary sibling Delete under attached $HOME failed: %v", err)
	}

	// Recreate a sibling file for move endpoint tests.
	testutil.FailErr(t, "rewrite sibling", os.WriteFile(srcSibling, []byte("hello"), 0o600))

	// 2. Native Delete targeting $HOME ("."), ~/.config (".config"), configdir, or symlink alias is refused.
	for _, target := range []string{".", home, ".config", dotConfig, "config-alias", aliasToConfig, ".config/paintedwolf-state"} {
		_, err := delTool.Delete(t.Context(), native.DeleteInput{
			Paths:     []string{target},
			Recursive: true,
		}, homeCtx)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != isolation.CodeControlPlaneDenied {
			t.Fatalf("Delete(%q) err = %v, want %s", target, err, isolation.CodeControlPlaneDenied)
		}
	}

	// 3. Native Move with source endpoint targeting ancestor or alias is refused.
	for _, src := range []string{".", home, ".config", dotConfig, "config-alias", aliasToConfig, ".config/paintedwolf-state"} {
		_, err := moveTool.Move(t.Context(), native.MoveInput{
			Pairs:       []native.FilePair{{From: src, To: "work/moved"}},
			Directories: true,
		}, homeCtx)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != isolation.CodeControlPlaneDenied {
			t.Fatalf("Move(from=%q) err = %v, want %s", src, err, isolation.CodeControlPlaneDenied)
		}
	}

	// 4. Native Move with destination endpoint targeting ancestor or alias is refused.
	for _, dst := range []string{".", home, ".config", dotConfig, "config-alias", aliasToConfig, ".config/paintedwolf-state"} {
		_, err := moveTool.Move(t.Context(), native.MoveInput{
			Pairs:       []native.FilePair{{From: "work/a.txt", To: dst}},
			Directories: true,
		}, homeCtx)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != isolation.CodeControlPlaneDenied {
			t.Fatalf("Move(to=%q) err = %v, want %s", dst, err, isolation.CodeControlPlaneDenied)
		}
	}

	// Verify state file remained untouched.
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("state file %q missing after ancestor delete/move attempts: %v", stateFile, err)
	}
	if _, err := os.Stat(dstSibling); !os.IsNotExist(err) {
		t.Fatalf("expected %q to remain deleted, got err=%v", dstSibling, err)
	}
}
