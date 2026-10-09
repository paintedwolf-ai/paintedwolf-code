package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMergeReconcilePathsAllowCoordinatorWrite(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "main.go")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(src), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(src, []byte("package main\n"), 0o644))

	profiles := []sandbox.ToolProfile{{
		ID:         "coordinator",
		WriteGlobs: []string{settingsoverlay.Rel("blueprints/**")},
	}}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, profiles)

	mgr := session.NewManager(sessionstore.NewMemory(), nil, nil, settings.SessionLimits{})
	mgr.Promotion.SetMergeReconcilePaths("sess-1", []string{"src/main.go"})
	boundary.SetMergeReconcileAllowlister(mgr.Promotion)

	ctx := sandbox.WithSessionID(context.Background(), "sess-1")
	if err := boundary.AssertWriteScope(ctx, root, "src/main.go", "coordinator"); err != nil {
		t.Fatalf("expected reconcile write allowed: %v", err)
	}
	if err := boundary.AssertWriteScope(ctx, root, "src/other.go", "coordinator"); err == nil {
		t.Fatal("expected write outside reconcile set to fail")
	}
}

func TestMergeReconcilePathsNotRegisteredForInvestigateProductWrite(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "foo.go")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(src), 0o755))

	mgr := session.NewManager(sessionstore.NewMemory(), nil, nil, settings.SessionLimits{})
	if mgr.Promotion.Allowed("sess-investigate", "src/foo.go") {
		t.Fatal("investigate product path must not be reconcile-allowed without SetMergeReconcilePaths")
	}
}
