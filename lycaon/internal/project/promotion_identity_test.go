package project_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPromotionPreservesRootIDAndAdoptsDestinationLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()
	reg := project.NewMemoryRegistry()

	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create draft", err)
	if len(p.Roots) != 1 {
		t.Fatalf("len(roots) = %d want 1", len(p.Roots))
	}
	rootID := p.Roots[0].ID
	scratch := p.Roots[0].Path
	testutil.FailErr(t, "seed scratch", os.WriteFile(filepath.Join(scratch, "a.txt"), []byte("x"), 0o644))

	dest := filepath.Join(t.TempDir(), "SOC Tools")
	testutil.FailErr(t, "create destination", os.Mkdir(dest, 0o755))
	engine := project.NewPromotionEngine(reg, nil)
	_, err = engine.Create(ctx, p.ID, dest, false)
	testutil.FailErr(t, "create promotion", err)
	promoted, err := engine.Run(ctx, p.ID)
	testutil.FailErr(t, "save draft root", err)
	if promoted.IsDraft {
		t.Fatal("expected saved project after relocate")
	}
	if len(promoted.Roots) != 1 {
		t.Fatalf("len(roots) = %d want 1", len(promoted.Roots))
	}
	if promoted.Roots[0].ID != rootID {
		t.Fatalf("root id = %q want %q", promoted.Roots[0].ID, rootID)
	}
	destResolved, _ := filepath.EvalSymlinks(dest)
	if promoted.Roots[0].Path != destResolved {
		t.Fatalf("path = %q want %q", promoted.Roots[0].Path, destResolved)
	}
	if promoted.Roots[0].Label != "SOC Tools" {
		t.Fatalf("label = %q want %q", promoted.Roots[0].Label, "SOC Tools")
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Fatalf("scratch dir still present: %v", statErr)
	}
}
