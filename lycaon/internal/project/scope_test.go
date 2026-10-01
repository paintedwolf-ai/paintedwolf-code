package project

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDraftWorkspaceDirRequiresProjectID(t *testing.T) {
	if _, err := DraftWorkspaceDir("  "); err == nil {
		t.Fatal("expected missing project id error")
	}
}

func TestScopeForSessionNoRootsUsesDraftScratch(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	ctx := context.Background()
	reg := NewMemoryRegistry()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "reg.Create failed", err)
	if p.Roots[0].Label != "Draft" {
		t.Fatalf("draft root label = %q want Draft", p.Roots[0].Label)
	}
	sess := &api.Session{ProjectID: p.ID}
	scope, err := ScopeForSession(ctx, sess, reg)
	testutil.FailErr(t, "ScopeForSession failed", err)
	if !scope.HasRoots {
		t.Fatal("expected HasRoots=true for draft scratch workspace")
	}
	wantScratch := filepath.Join(configDir, "drafts", p.ID)
	if resolved, err := filepath.EvalSymlinks(wantScratch); err == nil {
		wantScratch = resolved
	}
	if scope.WorkspacePath != wantScratch {
		t.Fatalf("WorkspacePath = %q want %q", scope.WorkspacePath, wantScratch)
	}
	if scope.WorkspaceRootID != p.Roots[0].ID {
		t.Fatalf("WorkspaceRootID = %q want %q", scope.WorkspaceRootID, p.Roots[0].ID)
	}
	if scope.ProjectID != p.ID {
		t.Fatalf("ProjectID = %q want %q", scope.ProjectID, p.ID)
	}
}

func TestScopeForSessionPrimaryDefault(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryRegistry()
	dir := t.TempDir()
	p, err := reg.Create(ctx, CreateParams{Roots: []AttachRootParams{{Path: dir}}})
	testutil.FailErr(t, "reg.Create failed", err)
	sess := &api.Session{ProjectID: p.ID}
	scope, err := ScopeForSession(ctx, sess, reg)
	testutil.FailErr(t, "ScopeForSession failed", err)
	if !scope.HasRoots {
		t.Fatal("expected HasRoots=true")
	}
	wantPath, _ := filepath.EvalSymlinks(dir)
	if scope.WorkspacePath != wantPath {
		t.Fatalf("WorkspacePath = %q want %q", scope.WorkspacePath, wantPath)
	}
	if scope.WorkspaceRootID == "" {
		t.Fatal("expected primary WorkspaceRootID")
	}
}

func TestScopeForSessionExplicitRoot(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryRegistry()
	primary := t.TempDir()
	secondary := t.TempDir()
	p, err := reg.Create(ctx, CreateParams{Roots: []AttachRootParams{{Path: primary}}})
	testutil.FailErr(t, "reg.Create failed", err)
	change, err := reg.AttachRoot(ctx, p.ID, AttachRootParams{Path: secondary})
	testutil.FailErr(t, "reg.AttachRoot failed", err)
	p = change.After
	var secondaryID string
	secondaryPath := secondary
	if resolved, err := filepath.EvalSymlinks(secondary); err == nil {
		secondaryPath = resolved
	}
	for _, r := range p.Roots {
		if r.Path == secondaryPath {
			secondaryID = r.ID
			break
		}
	}
	if secondaryID == "" {
		t.Fatal("secondary root id missing")
	}
	sess := &api.Session{ProjectID: p.ID, WorkspaceRootID: secondaryID}
	scope, err := ScopeForSession(ctx, sess, reg)
	testutil.FailErr(t, "ScopeForSession failed", err)
	if scope.WorkspaceRootID != secondaryID {
		t.Fatalf("WorkspaceRootID = %q want %q", scope.WorkspaceRootID, secondaryID)
	}
	if scope.WorkspacePath != secondaryPath {
		t.Fatalf("WorkspacePath = %q want %q", scope.WorkspacePath, secondaryPath)
	}
}
