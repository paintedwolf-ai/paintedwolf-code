package blueprint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUpdateContentRefusesStaleBase(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	store := NewFileStoreForTest(projectDir)

	bp := &api.Blueprint{ProjectID: "p1", Title: "Rollout"}
	testutil.FailErr(t, "create blueprint", store.Create(t.Context(), bp))

	read := bp.Content
	other := "---\nstatus: draft\ntitle: Rollout\n---\n\nanother writer got here first\n"
	abs := filepath.Join(projectDir, filepath.FromSlash(bp.Path))
	testutil.FailErr(t, "concurrent write", os.WriteFile(abs, []byte(other), 0o644))

	_, err := store.UpdateContent(t.Context(), "p1", bp.Path, "stale replacement\n", ContentDigest(read))
	if !errors.Is(err, ErrContentChanged) {
		t.Fatalf("UpdateContent error = %v, want ErrContentChanged", err)
	}
	got, readErr := os.ReadFile(abs)
	testutil.FailErr(t, "read destination", readErr)
	if string(got) != other {
		t.Fatalf("refused update still changed the document: %q", got)
	}
	assertNoStagedBlueprints(t, filepath.Dir(abs))
}

// The matching digest commits, and the committed bytes are what Get reads back.
func TestUpdateContentCommitsOnMatchingDigest(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	store := NewFileStoreForTest(projectDir)

	bp := &api.Blueprint{ProjectID: "p1", Title: "Rollout"}
	testutil.FailErr(t, "create blueprint", store.Create(t.Context(), bp))

	next := fmt.Sprintf("---\nid: %s\nstatus: draft\ntitle: Rollout\n---\n\nrevised\n", bp.ID)
	updated, err := store.UpdateContent(t.Context(), "p1", bp.Path, next, ContentDigest(bp.Content))
	testutil.FailErr(t, "UpdateContent", err)
	if updated.UpdatedAt.IsZero() {
		t.Fatalf("update declared no updated_at: %q", updated.Content)
	}
	if want := SetUpdatedAtFrontmatter(next, updated.UpdatedAt); updated.Content != want {
		t.Fatalf("committed content = %q, want %q", updated.Content, want)
	}
	abs := filepath.Join(projectDir, filepath.FromSlash(bp.Path))
	got, err := os.ReadFile(abs)
	testutil.FailErr(t, "read destination", err)
	if string(got) != updated.Content {
		t.Fatalf("disk bytes = %q, Get read %q", got, updated.Content)
	}
	assertNoStagedBlueprints(t, filepath.Dir(abs))
}

// An update with no digest is rejected: an optional precondition is an escape
// hatch back to the lost update it prevents.
func TestUpdateContentRequiresADigest(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	store := NewFileStoreForTest(projectDir)

	bp := &api.Blueprint{ProjectID: "p1", Title: "Rollout"}
	testutil.FailErr(t, "create blueprint", store.Create(t.Context(), bp))

	if _, err := store.UpdateContent(t.Context(), "p1", bp.Path, "anything\n", "  "); err == nil {
		t.Fatal("UpdateContent accepted an empty precondition")
	}
}

// Create never replaces: a document that appeared at the minted path after the
// collision check belongs to another writer.
func TestCreateRefusesAPathTakenAfterTheCollisionCheck(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	store := NewFileStoreForTest(projectDir)

	first := &api.Blueprint{ProjectID: "p1", Title: "Rollout"}
	testutil.FailErr(t, "create first", store.Create(t.Context(), first))

	// Asking for the taken path directly exercises the commit's own refusal
	// rather than the collision check that would normally re-mint away from it.
	second := &api.Blueprint{ProjectID: "p1", Title: "Rollout", Path: first.Path}
	testutil.FailErr(t, "create second", store.Create(t.Context(), second))
	if second.Path == first.Path {
		t.Fatal("collision minted the same path twice")
	}
	got, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(first.Path)))
	testutil.FailErr(t, "read first", err)
	if string(got) != first.Content {
		t.Fatalf("second create overwrote the first: %q", got)
	}
}

func assertNoStagedBlueprints(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read blueprint directory", err)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("staged replacement leaked: %s", entry.Name())
		}
	}
}
