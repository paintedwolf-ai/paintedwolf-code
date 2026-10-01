package blueprint

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBlueprintManagerProjectsCanonicalApproval(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	blueprintStore := NewFileStoreForTest(projectDir)
	mgr := NewManager(blueprintStore)
	mgr.Approvals = NewApprovalStore(sqlDB, authzcontext.SQLRecorder(sqlDB))
	ctx := context.Background()
	testdbseed.InsertSession(t, sqlDB, "sess-approval", testdbseed.DefaultProjectID)

	p, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "auth refactor", "", "", "")
	testutil.FailErr(t, "mgr.Create failed", err)
	if p.Status != api.BlueprintStatusDraft {
		t.Fatalf("status = %q", p.Status)
	}
	if err := ValidateConventionPath(p.Path); err != nil {
		t.Fatalf("path = %q: %v", p.Path, err)
	}
	_, err = sqlDB.ExecContext(ctx, `INSERT INTO blueprint_approvals(project_id, path, content_digest, workflow_revision, status, approved_at, approved_via, approved_by_person_id, session_id) VALUES (?, ?, ?, 0, 'approved', ?, 'api', (SELECT id FROM people WHERE role = 'owner'), 'sess-approval')`,
		testdbseed.DefaultProjectID, p.Path, ContentDigest(p.Content), db.FormatTime(p.UpdatedAt))
	testutil.FailErr(t, "insert canonical approval", err)
	approved, err := mgr.Get(ctx, testdbseed.DefaultProjectID, p.Path)
	testutil.FailErr(t, "mgr.Get failed", err)
	if approved.Status != api.BlueprintStatusApproved {
		t.Fatalf("status = %q", approved.Status)
	}
	ok, err := mgr.IsApproved(ctx, testdbseed.DefaultProjectID, p.Path)
	if err != nil || !ok {
		t.Fatalf("IsApproved = %v err=%v", ok, err)
	}
}

func TestListBlueprintsCapsAtMax(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	store := NewFileStoreForTest(projectDir)
	mgr := NewManager(store)
	ctx := context.Background()

	for i := 0; i < MAX_PROJECT_BLUEPRINTS+5; i++ {
		_, err := mgr.Create(ctx, testdbseed.DefaultProjectID, fmt.Sprintf("bp-%d", i), "", "plan", "")
		testutil.FailErr(t, "Create", err)
	}
	out, truncated, err := mgr.List(ctx, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "List", err)
	if len(out) != MAX_PROJECT_BLUEPRINTS || !truncated {
		t.Fatalf("list len = %d truncated = %v, want %d truncated", len(out), truncated, MAX_PROJECT_BLUEPRINTS)
	}
}

func TestConventionBlueprintDefaultsDraftWithoutCanonicalApproval(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "options", ConventionPath("options-selection.md"), "options", "")
	testutil.FailErr(t, "Create", err)
	if bp.Path != ConventionPath("options-selection.md") {
		t.Fatalf("path = %q", bp.Path)
	}
	loaded, err := mgr.Get(ctx, testdbseed.DefaultProjectID, bp.Path)
	testutil.FailErr(t, "Get", err)
	if loaded.Status != api.BlueprintStatusDraft {
		t.Fatalf("status = %q", loaded.Status)
	}
}

func TestManagerUpdateTitleAndDelete(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Original", ConventionPath("rename-me.md"), "plan", "")
	testutil.FailErr(t, "Create", err)
	path := bp.Path

	title := "Renamed blueprint"
	updated, err := mgr.Update(ctx, testdbseed.DefaultProjectID, path, nil, &title)
	testutil.FailErr(t, "title-only Update", err)
	if updated.Path != path {
		t.Fatalf("path changed: %q → %q", path, updated.Path)
	}
	if updated.Title != title {
		t.Fatalf("title = %q want %q", updated.Title, title)
	}
	if ParseTitleFrontmatter(updated.Content) != title {
		t.Fatalf("frontmatter title = %q", ParseTitleFrontmatter(updated.Content))
	}

	body := "# Body only\n\n## Goal\nkeep\n"
	contentOnly, err := mgr.Update(ctx, testdbseed.DefaultProjectID, path, &body, nil)
	testutil.FailErr(t, "content-only Update", err)
	if contentOnly.Title != title {
		t.Fatalf("content-only lost title: %q", contentOnly.Title)
	}

	empty := "   "
	if _, err := mgr.Update(ctx, testdbseed.DefaultProjectID, path, nil, &empty); err == nil {
		t.Fatal("empty title must fail")
	}
	bad := "line\nbreak"
	if _, err := mgr.Update(ctx, testdbseed.DefaultProjectID, path, nil, &bad); err == nil {
		t.Fatal("control-char title must fail")
	}

	testutil.FailErr(t, "Delete", mgr.Delete(ctx, testdbseed.DefaultProjectID, path))
	if _, err := mgr.Get(ctx, testdbseed.DefaultProjectID, path); err == nil {
		t.Fatal("Get after Delete must fail")
	}
	if err := mgr.Delete(ctx, testdbseed.DefaultProjectID, path); err == nil {
		t.Fatal("second Delete must fail")
	}
}
