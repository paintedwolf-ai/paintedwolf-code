package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

// runObservedPromotion promotes a one-root draft to a folder named
// "promoted-app" and returns whether the roots-changed observer fired, plus
// the root sets it received.
func runObservedPromotion(t *testing.T, reg Registry) (fired bool, before, after []projectroot.RootRef) {
	t.Helper()
	ctx := t.Context()
	p, err := reg.Create(ctx, CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(p.Roots[0].Path, "idea.txt"), []byte("complete"), 0o600))
	destination := filepath.Join(t.TempDir(), "promoted-app")
	testutil.FailErr(t, "create destination", os.MkdirAll(destination, 0o755))

	observer, ok := reg.(interface{ SetRootsChangedObserver(RootsChangedObserver) })
	if !ok {
		t.Fatal("registry does not expose SetRootsChangedObserver")
	}
	observer.SetRootsChangedObserver(func(_ context.Context, projectID string, b, a []projectroot.RootRef) {
		if projectID != p.ID {
			t.Errorf("observer project = %q want %q", projectID, p.ID)
		}
		fired = true
		before, after = b, a
	})

	engine := NewPromotionEngine(reg, nil)
	if _, err := engine.Create(ctx, p.ID, destination, false); err != nil {
		testutil.FailErr(t, "create promotion", err)
	}
	if _, err := engine.Run(ctx, p.ID); err != nil {
		testutil.FailErr(t, "run promotion", err)
	}
	return fired, before, after
}

func TestPromotionCommitNotifiesRootsChangedObserver(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	fired, before, after := runObservedPromotion(t, NewMemoryRegistry())
	if !fired {
		t.Fatal("promotion commit did not notify the roots-changed observer")
	}
	change := projectroot.DiffRoots(before, after)
	if len(change.Added) != 0 || len(change.Retired) != 0 {
		t.Fatalf("promotion change = added %d retired %d, want a pure rename", len(change.Added), len(change.Retired))
	}
	if len(change.Renamed) != 1 || change.Renamed[0].OldLabel != "Draft" || change.Renamed[0].NewLabel != "promoted-app" {
		t.Fatalf("promotion renames = %+v, want @Draft -> @promoted-app", change.Renamed)
	}
	if !change.ModelVisible() {
		t.Fatal("draft promotion relabel must be model-visible")
	}
}

func TestSQLPromotionCommitNotifiesRootsChangedObserver(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	SetDefaultOpenPolicy(TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "promotion-observer.db")
	defer sqlDB.Close()
	fired, before, after := runObservedPromotion(t, NewSQLRegistry(sqlDB))
	if !fired {
		t.Fatal("promotion commit did not notify the roots-changed observer")
	}
	change := projectroot.DiffRoots(before, after)
	if len(change.Renamed) != 1 || change.Renamed[0].OldLabel != "Draft" || change.Renamed[0].NewLabel != "promoted-app" {
		t.Fatalf("promotion renames = %+v, want @Draft -> @promoted-app", change.Renamed)
	}
}
