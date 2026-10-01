package project_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type rejectingProjectOutbox struct{}

func (rejectingProjectOutbox) EnqueueProjectTx(context.Context, *sql.Tx, string, any) error {
	return errors.New("event enqueue failed")
}

func (rejectingProjectOutbox) Notify() {}

func TestDetachRootBumpsGeneration(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	dir := t.TempDir()
	second := filepath.Join(t.TempDir(), "secondary")
	if err := os.MkdirAll(second, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: dir}},
	})
	testutil.FailErr(t, "create project", err)
	change, err := reg.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: second})
	testutil.FailErr(t, "attach second root", err)
	p = change.After
	if p.RootsGeneration != 1 {
		t.Fatalf("generation after attach = %d want 1", p.RootsGeneration)
	}
	rootID := p.Roots[1].ID
	change, err = reg.DetachRoot(ctx, p.ID, rootID)
	testutil.FailErr(t, "detach root", err)
	p = change.After
	if p.RootsGeneration != 2 {
		t.Fatalf("generation after detach = %d want 2", p.RootsGeneration)
	}
}

func TestPatchPrimaryDemotesOthers(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	dir := t.TempDir()
	second := filepath.Join(t.TempDir(), "secondary")
	if err := os.MkdirAll(second, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: dir}, {Path: second}},
	})
	testutil.FailErr(t, "create project", err)
	secondaryID := p.Roots[1].ID
	wantPrimary := true
	change, err := reg.PatchRoot(ctx, p.ID, secondaryID, project.PatchRootParams{IsPrimary: &wantPrimary})
	testutil.FailErr(t, "patch primary", err)
	p = change.After
	if p.RootsGeneration != 1 {
		t.Fatalf("generation = %d want 1", p.RootsGeneration)
	}
	primaryCount := 0
	var primaryID string
	for _, r := range p.Roots {
		if r.IsPrimary {
			primaryCount++
			primaryID = r.ID
		}
	}
	if primaryCount != 1 || primaryID != secondaryID {
		t.Fatalf("roots = %+v want single primary %q", p.Roots, secondaryID)
	}
}

func TestSQLRegistryDetachRootBumpsGeneration(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	dir := t.TempDir()
	second := filepath.Join(t.TempDir(), "secondary")
	if err := os.MkdirAll(second, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	sqlDB := testdbfixture.Open(t, "roots-lifecycle.db")
	reg := project.NewSQLRegistry(sqlDB)
	p, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: dir}},
	})
	testutil.FailErr(t, "create project", err)
	change, err := reg.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: second})
	testutil.FailErr(t, "attach root", err)
	p = change.After
	detachedID := p.Roots[0].ID
	replacementID := p.Roots[1].ID
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO sessions (
			id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at
		) VALUES ('detach-session', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`, p.ID, detachedID)
	testutil.FailErr(t, "insert bound session", err)
	change, err = reg.DetachRoot(ctx, p.ID, detachedID)
	testutil.FailErr(t, "detach root", err)
	p = change.After
	if p.RootsGeneration != 2 {
		t.Fatalf("generation = %d want 2", p.RootsGeneration)
	}
	var gotRootID string
	testutil.FailErr(t, "read reassigned root", sqlDB.QueryRowContext(ctx,
		`SELECT workspace_root_id FROM sessions WHERE id = 'detach-session'`).Scan(&gotRootID))
	if gotRootID != replacementID {
		t.Fatalf("workspace_root_id = %q want replacement %q", gotRootID, replacementID)
	}
}

func TestSQLLifecycleMutationsRefuseBusyProjectInsideTransaction(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "roots-busy.db")
	reg := project.NewSQLRegistry(sqlDB)
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: t.TempDir()}, {Path: t.TempDir()},
	}})
	testutil.FailErr(t, "create project", err)
	busyRootID := p.Roots[1].ID
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO sessions (
			id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at
		) VALUES ('busy-session', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'busy', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`, p.ID, busyRootID)
	testutil.FailErr(t, "insert busy session", err)

	_, err = reg.DetachRoot(ctx, p.ID, busyRootID)
	if !errors.Is(err, project.ErrRootBusy) {
		t.Fatalf("detach error = %v want ErrRootBusy", err)
	}
	if err := reg.Delete(ctx, p.ID); !errors.Is(err, project.ErrProjectBusy) {
		t.Fatalf("delete error = %v want ErrProjectBusy", err)
	}
	if _, err := reg.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: t.TempDir()}); !errors.Is(err, project.ErrProjectBusy) {
		t.Fatalf("attach error = %v want ErrProjectBusy", err)
	}
	renamed := "renamed"
	if _, err := reg.PatchRoot(ctx, p.ID, busyRootID, project.PatchRootParams{Label: &renamed}); !errors.Is(err, project.ErrProjectBusy) {
		t.Fatalf("patch root error = %v want ErrProjectBusy", err)
	}
	got, err := reg.Get(ctx, p.ID)
	testutil.FailErr(t, "get preserved project", err)
	if len(got.Roots) != 2 {
		t.Fatalf("preserved roots = %d want 2", len(got.Roots))
	}
}

func TestSQLLifecycleMutationsProtectDirtyEditorDocuments(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "dirty-document.db")
	reg := project.NewSQLRegistry(sqlDB)
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: t.TempDir()}, {Path: t.TempDir()},
	}})
	testutil.FailErr(t, "create project", err)
	root := p.Roots[1]
	now := time.Now().UTC()
	fileID := uuid.NewString()
	_, err = sqlDB.ExecContext(ctx, `INSERT INTO source_files (id, project_id, entry_kind, created_ts) VALUES (?, ?, 'file', ?)`, fileID, p.ID, now.Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert logical file", err)
	document := &editordoc.Document{
		ID: "document-1", ProjectID: p.ID, FileID: fileID,
		RootID: root.ID, Path: "notes.txt",
		Draft: "unsaved", BaseContent: "saved", BaseSHA256: "base", Encoding: "utf-8",
		SizeBytes: 5, EOL: "lf", BaseEOL: "lf", Revision: 1, Dirty: true,
		CreatedAt: now, UpdatedAt: now,
	}
	testutil.FailErr(t, "insert dirty document", editordoc.NewStore(sqlDB).Insert(ctx, document))

	_, err = reg.DetachRoot(ctx, p.ID, root.ID)
	if !errors.Is(err, project.ErrRootBusy) {
		t.Fatalf("detach error = %v want ErrRootBusy", err)
	}
	if err := reg.Delete(ctx, p.ID); !errors.Is(err, project.ErrProjectBusy) {
		t.Fatalf("delete error = %v want ErrProjectBusy", err)
	}
	_, err = reg.DetachRoot(project.WithForcedLifecycle(ctx), p.ID, root.ID)
	testutil.FailErr(t, "forced detach", err)
	var count int
	testutil.FailErr(t, "count cascaded documents", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM editor_documents WHERE id = ?`, document.ID).Scan(&count))
	if count != 0 {
		t.Fatalf("document count = %d want 0", count)
	}
}

func TestSQLRootMutationCommitsWithProjectEvent(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "roots-events.db")
	reg := project.NewSQLRegistry(sqlDB)
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "create project", err)
	reg.SetEventOutbox(eventoutbox.New(sqlDB, events.NewMemoryHub()))
	_, err = reg.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: t.TempDir()})
	testutil.FailErr(t, "attach root", err)
	var count int
	testutil.FailErr(t, "count project event", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event_outbox WHERE topic = 'project' AND project_id = ?`, p.ID).Scan(&count))
	if count != 1 {
		t.Fatalf("project event count = %d want 1", count)
	}
}

func TestSQLProjectCreateAndPatchCommitTheirEvents(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "project-events.db")
	reg := project.NewSQLRegistry(sqlDB)
	reg.SetEventOutbox(eventoutbox.New(sqlDB, events.NewMemoryHub()))
	p, err := reg.Create(ctx, project.CreateParams{Name: "Initial", Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "create project", err)
	name := "Renamed"
	_, err = reg.Patch(ctx, p.ID, project.PatchParams{Name: &name})
	testutil.FailErr(t, "patch project", err)

	rows, err := sqlDB.QueryContext(ctx, `SELECT data_json FROM event_outbox WHERE project_id = ? ORDER BY id`, p.ID)
	testutil.FailErr(t, "query events", err)
	defer rows.Close()
	var got []api.ProjectEvent
	for rows.Next() {
		var raw string
		testutil.FailErr(t, "scan event", rows.Scan(&raw))
		var event api.ProjectEvent
		testutil.FailErr(t, "decode event", json.Unmarshal([]byte(raw), &event))
		got = append(got, event)
	}
	testutil.FailErr(t, "iterate events", rows.Err())
	if len(got) != 2 || got[0].Action != api.ProjectEventCreated || got[1].Action != api.ProjectEventUpdated {
		t.Fatalf("events = %#v", got)
	}
	if got[1].Project == nil || got[1].Project.Name == nil || *got[1].Project.Name != name {
		t.Fatalf("patched event = %#v", got[1])
	}
}

func TestSQLProjectTouchEventPreservesQueuedPromotion(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "project-promotion-touch.db")
	reg := project.NewSQLRegistry(sqlDB)
	reg.SetEventOutbox(eventoutbox.New(sqlDB, events.NewMemoryHub()))
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	destination := t.TempDir()
	_, err = reg.CreatePromotion(ctx, p.ID, destination, true)
	testutil.FailErr(t, "queue promotion", err)
	testutil.FailErr(t, "touch last opened", reg.TouchLastOpened(ctx, p.ID))

	var raw string
	testutil.FailErr(t, "read touch event", sqlDB.QueryRowContext(ctx, `
		SELECT data_json
		FROM event_outbox
		WHERE project_id = ? AND topic = 'project'
		ORDER BY id DESC
		LIMIT 1
	`, p.ID).Scan(&raw))
	var event api.ProjectEvent
	testutil.FailErr(t, "decode touch event", json.Unmarshal([]byte(raw), &event))
	if event.Project == nil || event.Project.Promotion == nil {
		t.Fatalf("touch event dropped queued promotion: %#v", event.Project)
	}
	if event.Project.Promotion.Phase != "queued" {
		t.Fatalf("promotion phase = %q want queued", event.Project.Promotion.Phase)
	}
	if !project.SamePath(event.Project.Promotion.DestinationPath, destination) {
		t.Fatalf("promotion destination = %q want %q", event.Project.Promotion.DestinationPath, destination)
	}
}

func TestSQLProjectCreateRollsBackWithoutItsEvent(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "project-create-rollback.db")
	reg := project.NewSQLRegistry(sqlDB)
	reg.SetEventOutbox(rejectingProjectOutbox{})
	_, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	if err == nil {
		t.Fatal("create succeeded without its project event")
	}
	var count int
	testutil.FailErr(t, "count projects", sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&count))
	if count != 0 {
		t.Fatalf("project count = %d", count)
	}
}

func TestSQLRootMutationRollsBackWhenEventCannotBeRecorded(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "roots-events-rollback.db")
	reg := project.NewSQLRegistry(sqlDB)
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "create project", err)
	reg.SetEventOutbox(rejectingProjectOutbox{})
	_, err = reg.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: t.TempDir()})
	if err == nil {
		t.Fatal("attach succeeded without its project event")
	}
	got, err := reg.Get(ctx, p.ID)
	testutil.FailErr(t, "get project", err)
	if len(got.Roots) != 1 || got.RootsGeneration != 0 {
		t.Fatalf("rolled-back project = %#v", got)
	}
}

func TestDetachRootNotFound(t *testing.T) {
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(context.Background(), project.CreateParams{})
	testutil.FailErr(t, "create project", err)
	_, err = reg.DetachRoot(context.Background(), p.ID, "missing-root")
	if !errors.Is(err, project.ErrRootNotFound) {
		t.Fatalf("err = %v want ErrRootNotFound", err)
	}
}

func TestDetachLastRootNoFolder(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	dir := t.TempDir()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: dir}},
	})
	testutil.FailErr(t, "create project", err)
	rootID := p.Roots[0].ID
	change, err := reg.DetachRoot(ctx, p.ID, rootID)
	testutil.FailErr(t, "detach last root", err)
	p = change.After
	if len(p.Roots) != 0 {
		t.Fatalf("roots = %d want 0", len(p.Roots))
	}
	if p.IsDraft {
		t.Error("removing folders must not turn an established project back into a draft")
	}
}

func TestRegistryRootLifecycleParity(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	for _, tc := range []struct {
		name string
		open func(*testing.T) project.Registry
	}{
		{name: "memory", open: func(*testing.T) project.Registry { return project.NewMemoryRegistry() }},
		{name: "sqlite", open: func(t *testing.T) project.Registry {
			sqlDB := testdbfixture.Open(t, "store.db")
			return project.NewSQLRegistry(sqlDB)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			reg := tc.open(t)
			first, second := t.TempDir(), t.TempDir()
			p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
				{Path: first, Label: "API"}, {Path: second, Label: "Web"},
			}})
			testutil.FailErr(t, "create project", err)
			beforeGeneration := p.RootsGeneration
			label := "Backend"
			change, err := reg.PatchRoot(ctx, p.ID, p.Roots[1].ID, project.PatchRootParams{Label: &label})
			testutil.FailErr(t, "rename root", err)
			if !change.Changed || change.RootContextChanged || change.Before.RootsGeneration != change.After.RootsGeneration {
				t.Fatalf("label change = %#v", change)
			}
			primary := true
			change, err = reg.PatchRoot(ctx, p.ID, p.Roots[0].ID, project.PatchRootParams{IsPrimary: &primary})
			testutil.FailErr(t, "primary no-op", err)
			if change.Changed || change.After.RootsGeneration != beforeGeneration {
				t.Fatalf("primary no-op = %#v", change)
			}
			duplicate := "api"
			_, err = reg.PatchRoot(ctx, p.ID, p.Roots[1].ID, project.PatchRootParams{Label: &duplicate})
			if !errors.Is(err, project.ErrDuplicateRootLabel) {
				t.Fatalf("case-insensitive duplicate = %v", err)
			}
		})
	}
}

func TestDraftRootRejectsGenericLifecycleRoutes(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(t.Context(), project.CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	_, err = reg.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: t.TempDir()})
	if !errors.Is(err, project.ErrDraftRootImmutable) {
		t.Fatalf("attach error = %v", err)
	}
	_, err = reg.DetachRoot(t.Context(), p.ID, p.Roots[0].ID)
	if !errors.Is(err, project.ErrDraftRootImmutable) {
		t.Fatalf("detach error = %v", err)
	}
	label := "other"
	_, err = reg.PatchRoot(t.Context(), p.ID, p.Roots[0].ID, project.PatchRootParams{Label: &label})
	if !errors.Is(err, project.ErrDraftRootImmutable) {
		t.Fatalf("patch error = %v", err)
	}
}
