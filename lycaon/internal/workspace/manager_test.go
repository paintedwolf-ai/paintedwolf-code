package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestCreateWorkerWorkspaceIsolatedCopy(t *testing.T) {
	primary := t.TempDir()
	src := filepath.Join(primary, "src", "a.go")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		testutil.FailErr(t, "mkdir src", err)
	}
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		testutil.FailErr(t, "write src", err)
	}
	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "job-1")
	testutil.FailErr(t, "CreateWorkerWorkspace", err)
	if binding == nil || binding.Root == "" {
		t.Fatal("missing sandbox binding")
	}
	overlayFile := filepath.Join(binding.Root, "src", "a.go")
	if err := os.WriteFile(overlayFile, []byte("v2"), 0o644); err != nil {
		testutil.FailErr(t, "write overlay", err)
	}
	primaryBytes, _ := os.ReadFile(src)
	overlayBytes, _ := os.ReadFile(overlayFile)
	if string(primaryBytes) != "v1" || string(overlayBytes) != "v2" {
		t.Fatalf("primary=%q overlay=%q", primaryBytes, overlayBytes)
	}

	if err := mgr.DestroyWorkerWorkspace(binding); err != nil {
		testutil.FailErr(t, "DestroyWorkerWorkspace", err)
	}
	if _, err := os.Stat(binding.Root); !os.IsNotExist(err) {
		t.Fatal("sandbox root should be removed")
	}
}

func TestCreateWorkerWorkspaceLivesOutsideRepo(t *testing.T) {
	primary := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write main.go", err)
	}
	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "job-out")
	testutil.FailErr(t, "CreateWorkerWorkspace", err)
	if rel, err := filepath.Rel(primary, binding.Root); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("sandbox %q must live outside the project tree %q", binding.Root, primary)
	}
	if _, err := os.Stat(filepath.Join(primary, settingsoverlay.DirName())); !os.IsNotExist(err) {
		t.Fatal("provisioning must not write into the project repo")
	}
}

func TestDestroyWorkerWorkspaceNil(t *testing.T) {
	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	if err := mgr.DestroyWorkerWorkspace(nil); err != nil {
		testutil.FailErr(t, "DestroyWorkerWorkspace nil", err)
	}
	if err := mgr.DestroyWorkerWorkspace(&workspace.Binding{}); err != nil {
		testutil.FailErr(t, "DestroyWorkerWorkspace empty", err)
	}
}
