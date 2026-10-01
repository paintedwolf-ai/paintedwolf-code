package project_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

func TestResolveOverlaySingleRoot(t *testing.T) {
	dir := t.TempDir()
	roots := []projectroot.RootRef{{
		ID: "r1", Path: dir, IsPrimary: true, Label: "main",
	}}
	resolution, err := project.ResolveOverlay(roots, "")
	if err != nil {
		t.Fatalf("ResolveOverlay: %v", err)
	}
	got := resolution.Paths
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("got %v want [%q]", got, dir)
	}
}

func TestResolveOverlayPrimaryAndActive(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	secondary := filepath.Join(t.TempDir(), "secondary")
	roots := []projectroot.RootRef{
		{ID: "p", Path: primary, IsPrimary: true, Label: "main"},
		{ID: "s", Path: secondary, IsPrimary: false, Label: "api"},
	}
	resolution, err := project.ResolveOverlay(roots, "s")
	if err != nil {
		t.Fatalf("ResolveOverlay: %v", err)
	}
	got := resolution.Paths
	if len(got) != 2 {
		t.Fatalf("len = %d want 2", len(got))
	}
	if got[0] != primary || got[1] != secondary {
		t.Fatalf("got %v want [%q %q]", got, primary, secondary)
	}
}

func TestResolveOverlayPrimaryIsActive(t *testing.T) {
	dir := t.TempDir()
	roots := []projectroot.RootRef{{ID: "p", Path: dir, IsPrimary: true}}
	resolution, err := project.ResolveOverlay(roots, "p")
	if err != nil {
		t.Fatalf("ResolveOverlay: %v", err)
	}
	got := resolution.Paths
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("got %v want [%q]", got, dir)
	}
}

func TestResolveOverlayChecksEveryFormat(t *testing.T) {
	primary := t.TempDir()
	active := t.TempDir()
	writeOverlayFormat(t, primary, "overlay_format: 1\n")
	writeOverlayFormat(t, active, "overlay_format: 99\n")

	resolution, err := project.ResolveOverlay([]projectroot.RootRef{
		{ID: "primary", Path: primary, IsPrimary: true},
		{ID: "active", Path: active},
	}, "active")
	if err != nil {
		t.Fatalf("ResolveOverlay: %v", err)
	}
	if err := resolution.CheckCompatibility(); err == nil {
		t.Fatal("expected incompatible active overlay")
	}
	if len(resolution.Paths) != 2 || resolution.Paths[0] != primary || resolution.Paths[1] != active {
		t.Fatalf("Paths = %v want [%q %q]", resolution.Paths, primary, active)
	}
}

func TestOverlayCacheKey(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	key := project.OverlayCacheKey([]string{a, b})
	if key == "" {
		t.Fatal("expected non-empty key")
	}
	if project.OverlayCacheKey(nil) != "" {
		t.Fatal("nil paths")
	}
}

func writeOverlayFormat(t *testing.T, root, body string) {
	t.Helper()
	dir := settingsoverlay.Dir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, settingsoverlay.FormatFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write overlay format: %v", err)
	}
}
