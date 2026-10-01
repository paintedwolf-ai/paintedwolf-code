package project

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMemoryRegistryCreateAndGet(t *testing.T) {
	reg := NewMemoryRegistry()
	ctx := context.Background()
	dir := t.TempDir()

	p, err := CreateWithRoot(ctx, reg, dir)
	if err != nil {
		t.Fatalf("CreateWithRoot: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected project id")
	}
	if len(p.Roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(p.Roots))
	}
	if !p.Roots[0].IsPrimary {
		t.Fatal("first root should be primary")
	}

	got, err := reg.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != p.ID {
		t.Fatalf("id = %q want %q", got.ID, p.ID)
	}
}

func TestMemoryRegistryCreateNoRoots(t *testing.T) {
	reg := NewMemoryRegistry()
	p, err := reg.Create(context.Background(), CreateParams{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(p.Roots) != 0 {
		t.Fatalf("roots = %d, want 0", len(p.Roots))
	}
}

func TestMemoryRegistryCreateDraftThenPromote(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	reg := NewMemoryRegistry()
	ctx := context.Background()

	draft, err := reg.Create(ctx, CreateParams{Draft: true})
	if err != nil {
		t.Fatalf("Create draft: %v", err)
	}
	if !draft.IsDraft {
		t.Fatal("created project should be a draft")
	}

	destination := t.TempDir()
	engine := NewPromotionEngine(reg, nil)
	_, err = engine.Create(ctx, draft.ID, destination, false)
	if err != nil {
		t.Fatalf("Create promotion: %v", err)
	}
	promoted, err := engine.Run(ctx, draft.ID)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if promoted.IsDraft {
		t.Fatal("promoted project should no longer be a draft")
	}

	got, err := reg.Get(ctx, draft.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IsDraft {
		t.Fatal("promote should persist (draft → saved)")
	}
}

func TestMemoryRegistryPatchAppliesMetadataSet(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	reg := NewMemoryRegistry()
	ctx := t.Context()
	draft, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	name := "Release work"
	starred := true
	patched, err := reg.Patch(ctx, draft.ID, PatchParams{
		Name:    &name,
		Starred: &starred,
	})
	testutil.FailErr(t, "patch project", err)
	if patched.Name != name || !patched.Starred || !patched.IsDraft {
		t.Fatalf("patched project = %#v", patched)
	}
}

func TestMemoryRegistryAttachDuplicatePath(t *testing.T) {
	reg := NewMemoryRegistry()
	ctx := context.Background()
	dir := t.TempDir()
	p, err := CreateWithRoot(ctx, reg, dir)
	if err != nil {
		t.Fatalf("CreateWithRoot: %v", err)
	}
	_, err = reg.AttachRoot(ctx, p.ID, AttachRootParams{Path: dir})
	if !errors.Is(err, ErrDuplicateRoot) {
		t.Fatalf("err = %v, want ErrDuplicateRoot", err)
	}
}

func TestMemoryRegistryListRecentFirst(t *testing.T) {
	reg := NewMemoryRegistry()
	ctx := context.Background()
	first, err := reg.Create(ctx, CreateParams{})
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := reg.Create(ctx, CreateParams{})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	reg.mu.Lock()
	reg.projects[first.ID].LastOpenedAt = second.LastOpenedAt.Add(-time.Hour)
	reg.mu.Unlock()

	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].ID != second.ID {
		t.Fatalf("first listed = %q want %q", list[0].ID, second.ID)
	}
}

func TestPrimaryRootPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	p := &Project{Roots: []Root{{Path: dir, IsPrimary: true}}}
	if got := PrimaryRootPath(p); got != dir {
		t.Fatalf("PrimaryRootPath = %q want %q", got, dir)
	}
	if got := PrimaryRootPath(&Project{Roots: []Root{{Path: dir}}}); got != "" {
		t.Fatalf("path without primary = %q, want empty", got)
	}
}
