package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestProjectViewNeverObservesRollbackTransient pauses a project overlay write
// inside Validate, after Apply put a transient row on disk, and starts a
// projectView read. The read must join the writer's transaction lock and never
// observe the row the rollback removes.
func TestProjectViewNeverObservesRollbackTransient(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.DistroMCP: "providers:\n  - id: p1\n    command: \"true\"\n    args: []\n    enabled: false\n",
	})

	statePath := t.TempDir()
	projectDir := t.TempDir()

	r, err := NewRuntime(RuntimeOptions{
		StatePath:          statePath,
		GlobalOverridePath: t.TempDir() + "/mcp.yaml",
		Connector:          &MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	t.Cleanup(func() { _ = r.Close() })
	r.Catalog.SetProjectOverlayGate(func(context.Context, string) bool { return true })
	testutil.FailErr(t, "load", r.Catalog.Load(context.Background()))

	validateEntered := make(chan struct{})
	proceedValidate := make(chan struct{})
	readerAboutToRead := make(chan struct{})
	readerResult := make(chan projectCatalogView, 1)
	writeDone := make(chan error, 1)

	go func() {
		writeDone <- r.Administration.updateOverlay(context.Background(), projectDir,
			func(_ context.Context, path string) error {
				// The transient row an aborted transaction must never leak to a reader.
				return saveUserMCPConfig(path, UserMCPConfig{Providers: []MCPProviderOverlay{
					{ID: "p1", Enabled: boolPtr(true)},
				}})
			},
			func(context.Context) error {
				close(validateEntered)
				<-proceedValidate
				return errors.New("induced validation failure")
			},
		)
	}()

	<-validateEntered // Apply succeeded; the transient row is on disk, lock held.

	go func() {
		close(readerAboutToRead)
		readerResult <- r.Catalog.projectView(context.Background(), projectDir)
	}()
	<-readerAboutToRead

	close(proceedValidate) // Let Validate fail and the write roll back.

	if err := <-writeDone; err == nil {
		t.Fatal("expected the induced validation failure to surface")
	}
	view := <-readerResult
	entry, ok := view.entry("p1")
	if !ok {
		t.Fatal("p1 missing from the project view")
	}
	if entry.Enabled {
		t.Fatal("projectView observed the transient enabled=true row the rollback reverted")
	}
}

func boolPtr(b bool) *bool { return &b }
