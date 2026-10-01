package sandbox

import (
	"context"
	"os"
	"testing"

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
