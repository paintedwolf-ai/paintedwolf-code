package anchorcatalog_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func catalogPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	modRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(modRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
}

func TestLoadAndInstall(t *testing.T) {
	cat, err := anchorcatalog.Load(catalogPath(t))
	testutil.FailErr(t, "Load", err)
	if !cat.IDs["leg.finished"] || !cat.IDs["tool.post_invoke"] {
		t.Fatalf("expected core ids in catalog, got %d ids", len(cat.IDs))
	}
	d, ok := cat.Dedup["leg.finished"]
	if !ok || len(d.OmitInformWhen) != 1 || d.OmitInformWhen[0] != "board_reinjected" {
		t.Fatalf("leg.finished dedup=%#v", d)
	}
	anchorcatalog.Install(cat)
	if !anchorcatalog.Loaded() || !anchorcatalog.Has("leg.finished") {
		t.Fatal("Install did not set process catalog")
	}
	testutil.FailErr(t, "Require", anchorcatalog.Require("tool.pre_invoke"))
	err = anchorcatalog.Require("not.real")
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("want unknown id, got %v", err)
	}
}

func TestRequireBeforeInstall(t *testing.T) {
	path := catalogPath(t)
	anchorcatalog.Clear()
	t.Cleanup(func() { _ = anchorcatalog.InstallFile(path) })
	err := anchorcatalog.Require("leg.finished")
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("want not installed, got %v", err)
	}
}

func TestLoadRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.yaml")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte("schema_version: \"1.0\"\nanchors: []\n"), 0o600))
	_, err := anchorcatalog.Load(path)
	if err == nil || !strings.Contains(err.Error(), "no anchors") {
		t.Fatalf("want no anchors, got %v", err)
	}
}
