package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestPromotionEngineCompletesWithoutDeletingSourceEarly(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	reg := NewMemoryRegistry()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	source := p.Roots[0].Path
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(source, "idea.txt"), []byte("complete"), 0o600))
	destination := t.TempDir()
	engine := NewPromotionEngine(reg, nil)
	_, err = engine.Create(ctx, p.ID, destination, false)
	testutil.FailErr(t, "create promotion", err)
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source disappeared before commit: %v", err)
	}
	p, err = engine.Run(ctx, p.ID)
	testutil.FailErr(t, "run promotion", err)
	if p.IsDraft || p.Promotion != nil || p.Roots[0].Kind != RootKindAttached {
		t.Fatalf("promoted project = %#v", p)
	}
	if _, err := os.Stat(filepath.Join(destination, "idea.txt")); err != nil {
		t.Fatalf("installed file missing: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source retained after commit: %v", err)
	}
}

func TestPromotionCommitNamesOnlyAnUnnamedDraft(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	for _, tc := range []struct {
		name     string
		draft    string
		wantName func(string) string
	}{
		{name: "unnamed", wantName: filepath.Base},
		{name: "explicit", draft: "Research notes", wantName: func(string) string { return "Research notes" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewMemoryRegistry()
			p, err := reg.Create(ctx, CreateParams{Draft: true, Name: tc.draft})
			testutil.FailErr(t, "create draft", err)
			destination := t.TempDir()
			engine := NewPromotionEngine(reg, nil)
			_, err = engine.Create(ctx, p.ID, destination, false)
			testutil.FailErr(t, "create promotion", err)
			p, err = engine.Run(ctx, p.ID)
			testutil.FailErr(t, "run promotion", err)
			if p.Name != tc.wantName(destination) {
				t.Fatalf("project name = %q want %q", p.Name, tc.wantName(destination))
			}
		})
	}
}

func TestPromotionEngineRecoversInterruptedInstall(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := context.Background()
	reg := NewMemoryRegistry()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(p.Roots[0].Path, "idea.txt"), []byte("recover"), 0o600))
	destination := t.TempDir()
	engine := NewPromotionEngine(reg, nil)
	promotion, err := engine.Create(ctx, p.ID, destination, false)
	testutil.FailErr(t, "create promotion", err)
	testutil.FailErr(t, "copy stage", workspace.CopyTreeExact(ctx, promotion.SourcePath, promotion.StagePath))
	digest, err := workspace.TreeSHA256(ctx, promotion.StagePath)
	testutil.FailErr(t, "hash stage", err)
	_, err = reg.AdvancePromotion(ctx, p.ID, PromotionQueued, PromotionStaged, digest, digest, "")
	testutil.FailErr(t, "mark staged", err)
	testutil.FailErr(t, "reserve destination", os.Rename(destination, promotion.ReservationPath))
	testutil.FailErr(t, "install stage", os.Rename(promotion.StagePath, destination))

	p, err = engine.Run(ctx, p.ID)
	testutil.FailErr(t, "recover promotion", err)
	if p.IsDraft || p.Promotion != nil {
		t.Fatalf("recovered project = %#v", p)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "idea.txt")); err != nil || string(got) != "recover" {
		t.Fatalf("installed content = %q err=%v", got, err)
	}
}

func TestPromotionEngineRecoversAfterDestinationReservation(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	reg := NewMemoryRegistry()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(p.Roots[0].Path, "idea.txt"), []byte("resume rename"), 0o600))
	destination := t.TempDir()
	engine := NewPromotionEngine(reg, nil)
	promotion, err := engine.Create(ctx, p.ID, destination, false)
	testutil.FailErr(t, "create promotion", err)
	testutil.FailErr(t, "stage", engine.stage(ctx, promotion))
	promotion, err = reg.GetPromotion(ctx, p.ID)
	testutil.FailErr(t, "get staged promotion", err)
	testutil.FailErr(t, "reserve destination", os.Rename(destination, promotion.ReservationPath))

	p, err = engine.Run(ctx, p.ID)
	testutil.FailErr(t, "resume promotion", err)
	if p.IsDraft || p.Promotion != nil {
		t.Fatalf("resumed project = %#v", p)
	}
	if got, readErr := os.ReadFile(filepath.Join(destination, "idea.txt")); readErr != nil || string(got) != "resume rename" {
		t.Fatalf("installed content = %q err=%v", got, readErr)
	}
}

func TestPromotionEngineLeavesIntentAndSourceOnStageFailure(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	reg := NewMemoryRegistry()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	destination := t.TempDir()
	engine := NewPromotionEngine(reg, func(context.Context, string) error {
		return os.ErrPermission
	})
	_, err = engine.Create(ctx, p.ID, destination, true)
	testutil.FailErr(t, "create promotion", err)
	_, err = engine.Run(ctx, p.ID)
	if err == nil {
		t.Fatal("expected git initialization failure")
	}
	got, err := reg.Get(ctx, p.ID)
	testutil.FailErr(t, "get draft", err)
	if !got.IsDraft || got.Promotion == nil || got.Promotion.LastError == "" {
		t.Fatalf("failed promotion = %#v", got)
	}
	if _, err := os.Stat(got.Roots[0].Path); err != nil {
		t.Fatalf("source missing after failure: %v", err)
	}
}

func TestSQLPromotionRecoversCommittedCleanupAfterRestart(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open store", err)
	reg := NewSQLRegistry(sqlDB)
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	source := p.Roots[0].Path
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(source, "idea.txt"), []byte("restart"), 0o600))
	destination := t.TempDir()
	engine := NewPromotionEngine(reg, nil)
	promotion, err := engine.Create(ctx, p.ID, destination, false)
	testutil.FailErr(t, "create promotion", err)
	testutil.FailErr(t, "stage", engine.stage(ctx, promotion))
	promotion, err = reg.GetPromotion(ctx, p.ID)
	testutil.FailErr(t, "get staged", err)
	testutil.FailErr(t, "install", engine.install(ctx, promotion))
	promotion, err = reg.GetPromotion(ctx, p.ID)
	testutil.FailErr(t, "get installed", err)
	committed, err := engine.commit(ctx, promotion)
	testutil.FailErr(t, "commit", err)
	if committed.Promotion == nil || committed.Promotion.Phase != PromotionCommitted {
		t.Fatalf("committed project = %#v", committed)
	}
	if committed.Name != filepath.Base(destination) {
		t.Fatalf("committed name = %q want %q", committed.Name, filepath.Base(destination))
	}
	if committed.Roots[0].Label != filepath.Base(destination) {
		t.Fatalf("committed root label = %q want %q", committed.Roots[0].Label, filepath.Base(destination))
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source removed before committed cleanup: %v", err)
	}
	testutil.FailErr(t, "close store", sqlDB.Close())

	sqlDB, err = db.Open(dbPath)
	testutil.FailErr(t, "reopen store", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	reg = NewSQLRegistry(sqlDB)
	engine = NewPromotionEngine(reg, nil)
	p, err = engine.Run(ctx, p.ID)
	testutil.FailErr(t, "recover cleanup", err)
	if p.Promotion != nil || p.IsDraft {
		t.Fatalf("recovered project = %#v", p)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source remains after recovery cleanup: %v", err)
	}
}

func TestPromotionCancelRollsInstalledTreeBackToDraft(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	reg := NewMemoryRegistry()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(p.Roots[0].Path, "idea.txt"), []byte("cancel"), 0o600))
	destination := t.TempDir()
	engine := NewPromotionEngine(reg, nil)
	promotion, err := engine.Create(ctx, p.ID, destination, false)
	testutil.FailErr(t, "create promotion", err)
	testutil.FailErr(t, "stage", engine.stage(ctx, promotion))
	promotion, err = reg.GetPromotion(ctx, p.ID)
	testutil.FailErr(t, "get staged", err)
	testutil.FailErr(t, "install", engine.install(ctx, promotion))
	p, err = engine.Cancel(ctx, p.ID)
	testutil.FailErr(t, "cancel", err)
	if !p.IsDraft || p.Promotion != nil || p.Roots[0].Kind != RootKindDraft {
		t.Fatalf("canceled project = %#v", p)
	}
	empty, err := dirIsEmpty(destination)
	testutil.FailErr(t, "read destination", err)
	if !empty {
		t.Fatal("destination is not empty after cancellation")
	}
	if got, err := os.ReadFile(filepath.Join(p.Roots[0].Path, "idea.txt")); err != nil || string(got) != "cancel" {
		t.Fatalf("draft content = %q err=%v", got, err)
	}
}

func TestPromotionCancelPreservesDraftChangedAfterInstall(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	ctx := t.Context()
	reg := NewMemoryRegistry()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	sourceFile := filepath.Join(p.Roots[0].Path, "idea.txt")
	testutil.FailErr(t, "write draft", os.WriteFile(sourceFile, []byte("staged"), 0o600))
	destination := t.TempDir()
	engine := NewPromotionEngine(reg, nil)
	promotion, err := engine.Create(ctx, p.ID, destination, false)
	testutil.FailErr(t, "create promotion", err)
	testutil.FailErr(t, "stage", engine.stage(ctx, promotion))
	promotion, err = reg.GetPromotion(ctx, p.ID)
	testutil.FailErr(t, "get staged", err)
	testutil.FailErr(t, "install", engine.install(ctx, promotion))
	testutil.FailErr(t, "change draft", os.WriteFile(sourceFile, []byte("newer draft"), 0o600))

	p, err = engine.Cancel(ctx, p.ID)
	testutil.FailErr(t, "cancel", err)
	got, err := os.ReadFile(sourceFile)
	testutil.FailErr(t, "read preserved draft", err)
	if string(got) != "newer draft" || !p.IsDraft || p.Promotion != nil {
		t.Fatalf("canceled project = %#v content = %q", p, got)
	}
}

func TestPromotionPathExistsReturnsInspectionError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	testutil.FailErr(t, "write file", os.WriteFile(file, nil, 0o600))

	exists, err := promotionPathExists(filepath.Join(file, "child"))
	if err == nil {
		t.Fatal("expected path inspection error")
	}
	if exists {
		t.Fatal("unreadable path reported as present")
	}
}
