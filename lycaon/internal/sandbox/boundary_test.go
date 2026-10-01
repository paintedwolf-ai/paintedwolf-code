package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testBoundary(t *testing.T) *Boundary {
	t.Helper()
	return NewBoundary(Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []ToolProfile{
		{
			ID: "implement",
			Tools: map[string]bool{
				"read": true, "write": true, "edit": true,
			},
			DenyTools: []string{"command"},
		},
		{
			ID: "plan_writer",
			Tools: map[string]bool{
				"write": true,
			},
			WriteGlobs: []string{settingsoverlay.Rel("blueprints/**")},
		},
	})
}

// A per-session profile source that tightens a base profile's scope must be
// enforced at the path layer — AssertReadScope/AssertWriteScope/ExecMode — not
// just in tool allow/deny.
func TestScopeChecksHonorSessionProfileSource(t *testing.T) {
	root := t.TempDir()
	b := testBoundary(t)
	// Base "implement" has no read/write globs (unrestricted within root) and no
	// command tool (ExecMode none). The session overlay narrows scope to src/**
	// and grants command, so both the scope checks and ExecMode must change.
	b.SetProfileSource(func(_ context.Context, sessionID string) []ToolProfile {
		if sessionID != "s1" {
			return nil
		}
		return []ToolProfile{{
			ID:         "implement",
			Tools:      map[string]bool{"read": true, "write": true, "edit": true, "command": true},
			ReadGlobs:  []string{"src/**"},
			WriteGlobs: []string{"src/**"},
		}}
	})

	base := context.Background()
	session := WithSessionID(base, "s1")

	// Without a session, the boot profile applies: unrestricted within root.
	if err := b.AssertWriteScope(base, root, "docs/readme.md", "implement"); err != nil {
		t.Fatalf("boot profile must allow docs write: %v", err)
	}
	// With the tightening session overlay, a write outside src/** is refused.
	if err := b.AssertWriteScope(session, root, "docs/readme.md", "implement"); err == nil {
		t.Fatal("session overlay must refuse a write outside its narrowed scope")
	}
	if err := b.AssertWriteScope(session, root, "src/main.go", "implement"); err != nil {
		t.Fatalf("session overlay must allow a write inside its scope: %v", err)
	}
	if err := b.AssertReadScope(session, root, "docs/readme.md", "implement"); err == nil {
		t.Fatal("session overlay must refuse a read outside its narrowed scope")
	}
	// ExecMode reads the session-effective profile too: base lacks command, the
	// overlay grants it.
	if mode := b.ExecMode(base, "implement"); mode != ExecModeNone {
		t.Fatalf("boot ExecMode = %v want none", mode)
	}
	if mode := b.ExecMode(session, "implement"); mode != ExecModeAllowlisted {
		t.Fatalf("session overlay granted command, ExecMode = %v want allowlisted", mode)
	}
}

func TestSandboxPathValidation(t *testing.T) {
	root := t.TempDir()
	b := testBoundary(t)
	ctx := context.Background()

	for _, path := range []string{"README.md", "src/main.go", filepath.Join(root, "README.md")} {
		if err := b.AssertPathAllowed(ctx, root, path, PathOpRead); err != nil {
			t.Fatalf("valid path %q: %v", path, err)
		}
	}

	for _, path := range []string{"../secret.txt", "/etc/passwd", "src/../../secret.txt"} {
		if err := b.AssertPathAllowed(ctx, root, path, PathOpRead); err == nil {
			t.Fatalf("expected error for %q", path)
		}
	}
}

func TestSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not supported")
	}

	b := testBoundary(t)
	if err := b.AssertPathAllowed(context.Background(), root, "link.txt", PathOpRead); err == nil {
		t.Fatal("expected symlink escape error")
	}
}

// A write under a symlinked directory whose leaf directory does not exist yet is refused.
// EvalSymlinks on the missing parent returns IsNotExist, and a later MkdirAll would
// materialize that directory through the symlink, outside the root.
func TestSymlinkEscapeThroughMissingIntermediate(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "evil")); err != nil {
		t.Skip("symlinks not supported")
	}
	b := testBoundary(t)
	ctx := context.Background()

	// evil -> outside; "newdir" does not exist under outside. Must be rejected.
	if err := b.AssertPathAllowed(ctx, root, "evil/newdir/payload", PathOpWrite); err == nil {
		t.Fatal("expected symlink escape error for write through symlinked dir with missing intermediate")
	}

	// A genuinely in-root new path (no symlink) must still be allowed.
	if err := b.AssertPathAllowed(ctx, root, "sub/newdir/payload", PathOpWrite); err != nil {
		t.Fatalf("in-root new path should be allowed: %v", err)
	}

	// An in-root symlink pointing at an in-root directory, with a missing leaf, stays allowed.
	realDir := filepath.Join(root, "realdir")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir realdir", err)
	}
	if err := os.Symlink(realDir, filepath.Join(root, "inlink")); err != nil {
		t.Skip("symlinks not supported")
	}
	if err := b.AssertPathAllowed(ctx, root, "inlink/newfile", PathOpWrite); err != nil {
		t.Fatalf("in-root symlink to in-root dir should be allowed: %v", err)
	}
}

func TestWriteScopeGlobs(t *testing.T) {
	root := t.TempDir()
	b := testBoundary(t)
	ctx := context.Background()

	if err := b.AssertWriteScope(ctx, root, "src/main.go", "implement"); err != nil {
		t.Fatalf("implement write: %v", err)
	}
	if err := b.AssertWriteScope(ctx, root, "src/main.go", "plan_writer"); err == nil {
		t.Fatal("expected write scope error")
	}
	if err := b.AssertWriteScope(ctx, root, settingsoverlay.DirName()+"/blueprints/foo.md", "plan_writer"); err != nil {
		t.Fatalf("plan_writer allowed path: %v", err)
	}
}

func TestReadScopeGlobs(t *testing.T) {
	root := t.TempDir()
	b := NewBoundary(Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []ToolProfile{
		{
			ID:        "coordinator",
			Tools:     map[string]bool{"read": true},
			ReadGlobs: []string{settingsoverlay.Rel("blueprints/**"), settingsoverlay.Rel("blueprints/**/*.md")},
		},
		{
			ID:    "implement",
			Tools: map[string]bool{"read": true},
		},
	})
	ctx := context.Background()

	if err := b.AssertReadScope(ctx, root, "src/main.go", "coordinator"); err == nil {
		t.Fatal("expected coordinator read scope error for product path")
	}
	if err := b.AssertReadScope(ctx, root, settingsoverlay.DirName()+"/blueprints/foo.md", "coordinator"); err != nil {
		t.Fatalf("coordinator plan read: %v", err)
	}
	if err := b.AssertReadScope(ctx, root, "src/main.go", "implement"); err != nil {
		t.Fatalf("implement unrestricted read: %v", err)
	}
}

func TestAssertToolAllowed(t *testing.T) {
	b := testBoundary(t)
	ctx := context.Background()
	if err := b.AssertToolAllowed(ctx, "implement", "read", ToolAccessProfile); err != nil {
		testutil.FailErr(t, "b.AssertToolAllowed failed", err)
	}
	if err := b.AssertToolAllowed(ctx, "implement", "command", ToolAccessProfile); err == nil {
		t.Fatal("expected command denied")
	}
	if err := b.AssertToolAllowed(ctx, "implement", "future_tool", ToolAccessAll); err != nil {
		t.Fatalf("open-world access denied future tool: %v", err)
	}
}

func TestAssertWriteScopeTurnWritePin(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	b := testBoundary(t)

	pinned := WithTurnWritePin(context.Background(), TurnWritePin{
		RootPath: root,
		Globs:    []string{"src/target.go"},
	})
	testutil.FailErr(t, "pinned path allowed",
		b.AssertWriteScope(pinned, root, "src/target.go", "implement"))
	if err := b.AssertWriteScope(pinned, root, "src/other.go", "implement"); err == nil {
		t.Fatal("write outside pin should be rejected")
	}
	if err := b.AssertWriteScope(pinned, other, "src/target.go", "implement"); err == nil {
		t.Fatal("write under a different root should be rejected")
	}

	dirPinned := WithTurnWritePin(context.Background(), TurnWritePin{
		RootPath: root,
		Globs:    []string{"src/target.go", "src/*"},
	})
	testutil.FailErr(t, "sibling in pinned dir allowed",
		b.AssertWriteScope(dirPinned, root, "src/target_test.go", "implement"))
	if err := b.AssertWriteScope(dirPinned, root, "src/deep/nested.go", "implement"); err == nil {
		t.Fatal("nested path should not match non-recursive dir glob")
	}

	failClosed := WithTurnWritePin(context.Background(), TurnWritePin{Globs: []string{"src/target.go"}})
	if err := b.AssertWriteScope(failClosed, root, "src/target.go", "implement"); err == nil {
		t.Fatal("pin with empty root must deny all writes")
	}

	unpinned := context.Background()
	testutil.FailErr(t, "unpinned write allowed",
		b.AssertWriteScope(unpinned, root, "src/anything.go", "implement"))
}
