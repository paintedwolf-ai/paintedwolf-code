package project

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSQLRegistryPersistence(t *testing.T) {
	SetDefaultOpenPolicy(TestOpenPolicy())
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "store.db")

	sqlDB := testdbfixture.OpenPath(t, dbPath)
	reg := NewSQLRegistry(sqlDB)
	p1, err := CreateWithRoot(context.Background(), reg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	testutil.FailErr(t, "close database for reopen", sqlDB.Close())

	sqlDB2 := testdbfixture.OpenPath(t, dbPath)
	reg2 := NewSQLRegistry(sqlDB2)
	p2, err := reg2.Get(context.Background(), p1.ID)
	testutil.FailErr(t, "reg2.Get failed", err)
	if PrimaryRootPath(p2) != PrimaryRootPath(p1) {
		t.Fatalf("path = %q want %q", PrimaryRootPath(p2), PrimaryRootPath(p1))
	}
}

func TestSQLRegistryPatchAppliesMetadataSet(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "store.db")
	reg := NewSQLRegistry(sqlDB)
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

func TestSQLRegistryTrustSeenPersists(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")
	reg := NewSQLRegistry(sqlDB)
	p, err := CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)

	_, err = reg.MarkTrustSeen(t.Context(), p.ID, p, map[string]SeenRecord{
		"project_mcp": {Stamp: "stamp-1"},
	})
	testutil.FailErr(t, "MarkTrustSeen", err)

	got, err := reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "Get", err)
	if !SurfaceSeen(*got, "project_mcp", "stamp-1") {
		t.Fatalf("seen record did not survive the round trip: %+v", got.TrustSeen)
	}
}

// Seen records preserve the reported state across root changes.
func TestSQLRegistryTrustSeenSurvivesRootChange(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")
	reg := NewSQLRegistry(sqlDB)
	p, err := CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)
	_, err = reg.MarkTrustSeen(t.Context(), p.ID, p, map[string]SeenRecord{"skills": {Stamp: "s"}})
	testutil.FailErr(t, "MarkTrustSeen", err)

	_, err = reg.AttachRoot(t.Context(), p.ID, AttachRootParams{Path: t.TempDir()})
	testutil.FailErr(t, "AttachRoot", err)

	got, err := reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "Get", err)
	if !SurfaceSeen(*got, "skills", "s") {
		t.Fatalf("attaching a root discarded the seen record: %+v", got.TrustSeen)
	}
}

func TestSQLRegistryTrustEnabledPersists(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")
	reg := NewSQLRegistry(sqlDB)
	p, err := CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)

	_, err = reg.SetTrustEnabled(t.Context(), p.ID, map[string]bool{"scan_config": false})
	testutil.FailErr(t, "SetTrustEnabled", err)

	got, err := reg.Get(t.Context(), p.ID)
	testutil.FailErr(t, "Get", err)
	if TrustEnabled(*got, "scan_config") {
		t.Fatalf("trust_enabled did not persist: %+v", got.TrustEnabled)
	}
}

func TestSQLTrustBaselineBytesStayOutOfProjectRows(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	database := testdbfixture.Open(t, "store.db")
	registry := NewSQLRegistry(database)
	p, err := CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	record := SeenRecord{Stamp: "first", ReadAt: "2026-09-15T12:00:00Z", Files: []TrustReadFile{{RootID: p.Roots[0].ID, RootLabel: "Root", Path: "AGENTS.md", Content: "retained text", SHA256: "hash"}}, BeforeFiles: []TrustReadFile{{RootID: p.Roots[0].ID, Path: "AGENTS.md", Content: "previous text", SHA256: "previous"}}}
	updated, err := registry.MarkTrustSeen(t.Context(), p.ID, p, map[string]SeenRecord{"agents_md": record})
	testutil.FailErr(t, "capture baseline", err)
	if len(updated.TrustSeen["agents_md"].Files) != 0 || len(updated.TrustSeen["agents_md"].BeforeFiles) != 0 {
		t.Fatal("ordinary project response loaded configuration bodies")
	}
	reopened := NewSQLRegistry(database)
	baseline, err := reopened.ReadTrustBaseline(t.Context(), p.ID)
	testutil.FailErr(t, "read retained baseline", err)
	if len(baseline["agents_md"].Files) != 1 || baseline["agents_md"].Files[0].Content != "retained text" || len(baseline["agents_md"].BeforeFiles) != 1 || baseline["agents_md"].BeforeFiles[0].Content != "previous text" {
		t.Fatalf("retained baseline = %+v", baseline)
	}
	_, err = registry.MarkTrustSeen(t.Context(), p.ID, p, map[string]SeenRecord{"agents_md": {Stamp: "stale"}})
	if !errors.Is(err, ErrTrustReviewChanged) {
		t.Fatalf("stale capture = %v", err)
	}
	after, err := reopened.ReadTrustBaseline(t.Context(), p.ID)
	testutil.FailErr(t, "read baseline after conflict", err)
	if after["agents_md"].Files[0].Content != "retained text" {
		t.Fatal("conflict replaced retained bytes")
	}
}
