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
	for _, dir := range []string{filepath.Join(cfgDir, "packs"), scratchSessionDir, outputSessionDir} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	testutil.FailErr(t, "write settings.yaml", os.WriteFile(filepath.Join(cfgDir, "settings.yaml"), []byte("model: test\n"), 0o600))
	testutil.FailErr(t, "write custom.yaml", os.WriteFile(filepath.Join(cfgDir, "packs", "custom.yaml"), []byte("pack: true\n"), 0o600))
	testutil.FailErr(t, "write api.token", os.WriteFile(filepath.Join(cfgDir, "api.token"), []byte("secret-token\n"), 0o600))
	testutil.FailErr(t, "write scratch note", os.WriteFile(filepath.Join(scratchSessionDir, "note.txt"), []byte("scratch\n"), 0o600))
	testutil.FailErr(t, "write output log", os.WriteFile(filepath.Join(outputSessionDir, "out.txt"), []byte("output\n"), 0o600))

	boundary := testBoundary(t)
	ctx := context.Background()

	// Directly attached control directory is wholly denied by both CompileReadScope and AssertReadScope.
	cfgScope, err := boundary.CompileReadScope(ctx, cfgDir, "implement")
	testutil.FailErr(t, "compile cfgDir read scope", err)
	for _, rel := range []string{".", "settings.yaml", "packs/custom.yaml", "api.token", "scratch/sess-1/note.txt"} {
		if cfgScope.Filter(rel, false) {
			t.Fatalf("CompileReadScope on control dir allowed %q", rel)
		}
		if err := boundary.AssertReadScope(ctx, cfgDir, rel, "implement"); !errors.Is(err, ErrOutsideReadScope) {
			t.Fatalf("AssertReadScope(cfgDir, %q) = %v, want ErrOutsideReadScope", rel, err)
		}
	}

	// Concrete session scratch and session output roots are allowed for their own session files.
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
}
