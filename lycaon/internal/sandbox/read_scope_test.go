package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompileReadFilterFreezesEffectiveProfile(t *testing.T) {
	root := t.TempDir()
	boundary := testBoundary(t)
	lookups := 0
	boundary.SetProfileSource(func(_ context.Context, sessionID string) []ToolProfile {
		lookups++
		if sessionID != "session-1" {
			return nil
		}
		return []ToolProfile{{
			ID: "implement", Tools: map[string]bool{"read": true}, ReadGlobs: []string{"src/**"},
		}}
	})
	ctx := WithSessionID(context.Background(), "session-1")
	filter, err := boundary.CompileReadFilter(ctx, root, "implement")
	testutil.FailErr(t, "compile read filter", err)
	testutil.FailErr(t, "remove root", os.RemoveAll(root))
	for range 10_000 {
		if !filter("src/main.go", false) {
			t.Fatal("compiled filter rejected an in-scope path")
		}
		if filter("docs/readme.md", false) {
			t.Fatal("compiled filter allowed an out-of-scope path")
		}
	}
	if !filter("src", true) {
		t.Fatal("compiled filter pruned a directory that contains in-scope descendants")
	}
	if filter("docs", true) {
		t.Fatal("compiled filter allowed an unrelated directory")
	}
	if filter("../escape.txt", false) {
		t.Fatal("compiled filter allowed a path escape")
	}
	if !filter("src/cache.db", false) {
		t.Fatal("compiled filter rejected an in-scope database")
	}
	if lookups != 1 {
		t.Fatalf("profile source lookups = %d, want one for the full projection", lookups)
	}
}

func TestToolProfileSnapshotPinsEveryPathCheck(t *testing.T) {
	root := t.TempDir()
	boundary := testBoundary(t)
	lookups := 0
	boundary.SetProfileSource(func(_ context.Context, sessionID string) []ToolProfile {
		lookups++
		return []ToolProfile{{ID: "implement", ReadGlobs: []string{"src/**"}}}
	})
	ctx := WithSessionID(context.Background(), "session-1")
	ctx, err := boundary.WithToolProfileSnapshot(ctx, "implement")
	testutil.FailErr(t, "snapshot tool profile", err)
	for range 10_000 {
		testutil.FailErr(t, "check pinned read scope", boundary.AssertReadScope(ctx, root, "src/main.go", "implement"))
	}
	if lookups != 1 {
		t.Fatalf("profile source lookups = %d, want one for the invocation", lookups)
	}
}

func TestCompiledReadScopeKeyTracksEffectivePolicy(t *testing.T) {
	root := t.TempDir()
	boundary := testBoundary(t)
	profile := ToolProfile{ID: "implement", ReadGlobs: []string{"src/**"}}
	boundary.SetProfileSource(func(context.Context, string) []ToolProfile { return []ToolProfile{profile} })
	ctx := WithSessionID(context.Background(), "session-1")
	first, err := boundary.CompileReadScope(ctx, root, "implement")
	testutil.FailErr(t, "compile first scope", err)
	profile.ReadGlobs = []string{"docs/**"}
	second, err := boundary.CompileReadScope(ctx, root, "implement")
	testutil.FailErr(t, "compile second scope", err)
	if first.Key == second.Key {
		t.Fatal("different effective policies shared a read-scope cache key")
	}
}

func TestCompileAndAssertReadScopeControlPlaneAndSessionScratch(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	scratchSessionDir := filepath.Join(cfgDir, "scratch", "sess-1")
	outputSessionDir := filepath.Join(cfgDir, "session-output", "sess-1")
	otherScratchDir := filepath.Join(cfgDir, "scratch", "other-session")
	wtDir := filepath.Join(cfgDir, "worktrees", "wt-1")
	for _, dir := range []string{filepath.Join(cfgDir, "packs"), scratchSessionDir, outputSessionDir, otherScratchDir, wtDir} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	testutil.FailErr(t, "write settings.yaml", os.WriteFile(filepath.Join(cfgDir, "settings.yaml"), []byte("model: test\n"), 0o600))
	testutil.FailErr(t, "write custom.yaml", os.WriteFile(filepath.Join(cfgDir, "packs", "custom.yaml"), []byte("pack: true\n"), 0o600))
	testutil.FailErr(t, "write api.token", os.WriteFile(filepath.Join(cfgDir, "api.token"), []byte("secret-token\n"), 0o600))
	testutil.FailErr(t, "write scratch note", os.WriteFile(filepath.Join(scratchSessionDir, "note.txt"), []byte("scratch\n"), 0o600))
	testutil.FailErr(t, "write output log", os.WriteFile(filepath.Join(outputSessionDir, "out.txt"), []byte("output\n"), 0o600))
	testutil.FailErr(t, "write other scratch note", os.WriteFile(filepath.Join(otherScratchDir, "secret.txt"), []byte("other-session-secret\n"), 0o600))
	testutil.FailErr(t, "write wt file", os.WriteFile(filepath.Join(wtDir, "main.go"), []byte("package main\n"), 0o600))

	boundary := testBoundary(t)
	ctx := WithSessionDirs(context.Background(), scratchSessionDir, outputSessionDir)

	// 1. Directly attached control directory is wholly denied by both CompileReadScope and AssertReadScope.
	cfgScope, err := boundary.CompileReadScope(ctx, cfgDir, "implement")
	testutil.FailErr(t, "compile cfgDir read scope", err)
	for _, rel := range []string{".", "settings.yaml", "packs/custom.yaml", "api.token", "scratch/sess-1/note.txt"} {
		if cfgScope.Filter(rel, false) {
			t.Fatalf("CompileReadScope on control dir allowed %q", rel)
		}
		var scopeErr *ScopeError
		if err := boundary.AssertReadScope(ctx, cfgDir, rel, "implement"); !errors.As(err, &scopeErr) || scopeErr.Kind != ScopeRead {
			t.Fatalf("AssertReadScope(cfgDir, %q) = %v, want ScopeError(ScopeRead)", rel, err)
		}
	}

	// 2. Invocation-owned session scratch and session output roots are allowed for their own session files.
	for _, tc := range []struct {
		dir  string
		file string
	}{
		{dir: scratchSessionDir, file: "note.txt"},
		{dir: outputSessionDir, file: "out.txt"},
	} {
		scope, err := boundary.CompileReadScope(ctx, tc.dir, "implement")
		testutil.FailErr(t, "compile session dir read scope", err)
		if !scope.Filter(tc.file, false) {
			t.Fatalf("CompileReadScope on %s rejected %q", tc.dir, tc.file)
		}
		if err := boundary.AssertReadScope(ctx, tc.dir, tc.file, "implement"); err != nil {
			t.Fatalf("AssertReadScope(%s, %q) failed: %v", tc.dir, tc.file, err)
		}
	}

	// 3. Another session's scratch root (<cfgDir>/scratch/other-session) or an unowned scratch root
	// is denied by both CompileReadScope and AssertReadScope.
	for _, tc := range []struct {
		ctx  context.Context
		dir  string
		file string
	}{
		{ctx: ctx, dir: otherScratchDir, file: "secret.txt"},
		{ctx: context.Background(), dir: scratchSessionDir, file: "note.txt"},
	} {
		scope, err := boundary.CompileReadScope(tc.ctx, tc.dir, "implement")
		testutil.FailErr(t, "compile unowned session dir read scope", err)
		if scope.Filter(tc.file, false) {
			t.Fatalf("CompileReadScope on unowned session dir %s allowed %q", tc.dir, tc.file)
		}
		var scopeErr *ScopeError
		if err := boundary.AssertReadScope(tc.ctx, tc.dir, tc.file, "implement"); !errors.As(err, &scopeErr) || scopeErr.Kind != ScopeRead {
			t.Fatalf("AssertReadScope(%s, %q) = %v, want ScopeError(ScopeRead)", tc.dir, tc.file, err)
		}
	}

	// 4. Managed workspace (<cfgDir>/worktrees/wt-1) explicitly covered by LYCAON_SANDBOX_DENY_READ
	// is denied by both CompileReadScope and AssertReadScope.
	t.Setenv("LYCAON_SANDBOX_DENY_READ", wtDir)
	wtScope, err := boundary.CompileReadScope(ctx, wtDir, "implement")
	testutil.FailErr(t, "compile denied wtDir read scope", err)
	if wtScope.Filter("main.go", false) {
		t.Fatal("CompileReadScope on LYCAON_SANDBOX_DENY_READ-covered managed workspace allowed main.go")
	}
	var scopeErr *ScopeError
	if err := boundary.AssertReadScope(ctx, wtDir, "main.go", "implement"); !errors.As(err, &scopeErr) || scopeErr.Kind != ScopeRead {
		t.Fatalf("AssertReadScope(wtDir, main.go) = %v, want ScopeError(ScopeRead)", err)
	}
}
