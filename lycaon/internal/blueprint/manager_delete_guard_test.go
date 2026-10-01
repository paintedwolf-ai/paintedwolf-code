package blueprint

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

// A blueprint a live run is executing is not deletable: the run's
// blueprint_path would dangle, and the run was authorized against a document
// that no longer exists.
func TestDeleteRefusesWhileARunIsUsingTheBlueprint(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Live", ConventionPath("live-plan.md"), "plan", "")
	testutil.FailErr(t, "Create", err)

	mgr.ActiveRun = func(context.Context, string, string) (string, bool, error) {
		return "run-42", true, nil
	}
	err = mgr.Delete(ctx, testdbseed.DefaultProjectID, bp.Path)
	if !errors.Is(err, ErrRunActive) {
		t.Fatalf("Delete error = %v, want ErrRunActive", err)
	}
	if _, getErr := mgr.Get(ctx, testdbseed.DefaultProjectID, bp.Path); getErr != nil {
		t.Fatalf("a refused delete must leave the blueprint readable: %v", getErr)
	}

	mgr.ActiveRun = func(context.Context, string, string) (string, bool, error) {
		return "", false, nil
	}
	testutil.FailErr(t, "Delete after the run ended", mgr.Delete(ctx, testdbseed.DefaultProjectID, bp.Path))
	if _, getErr := mgr.Get(ctx, testdbseed.DefaultProjectID, bp.Path); getErr == nil {
		t.Fatal("blueprint still readable after a permitted delete")
	}
}

// An unwired hook must not silently permit the delete path to skip its check
// in production, but it also must not break hosts that never wire it.
func TestDeleteProceedsWhenTheRunCheckIsNotWired(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Unwired", ConventionPath("unwired.md"), "plan", "")
	testutil.FailErr(t, "Create", err)
	testutil.FailErr(t, "Delete with no run check", mgr.Delete(ctx, testdbseed.DefaultProjectID, bp.Path))
}

// A failing lookup is not a permission to delete.
func TestDeleteSurfacesRunCheckFailure(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Broken", ConventionPath("broken.md"), "plan", "")
	testutil.FailErr(t, "Create", err)

	boom := errors.New("run lookup failed")
	mgr.ActiveRun = func(context.Context, string, string) (string, bool, error) {
		return "", false, boom
	}
	if err := mgr.Delete(ctx, testdbseed.DefaultProjectID, bp.Path); !errors.Is(err, boom) {
		t.Fatalf("Delete error = %v, want the lookup failure", err)
	}
	if _, getErr := mgr.Get(ctx, testdbseed.DefaultProjectID, bp.Path); getErr != nil {
		t.Fatalf("a failed check must leave the blueprint readable: %v", getErr)
	}
}
