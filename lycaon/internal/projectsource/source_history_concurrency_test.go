package projectsource

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestNativeRemovalOrdersAfterAnInFlightUndo(t *testing.T) {
	service := NewSourceMutationService(nil, nil)
	root := t.TempDir()
	project := &Project{ID: "project", Roots: []Root{{ID: "root", ProjectID: "project", Path: root, IsPrimary: true}}}
	for _, name := range []string{"a", "b"} {
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, name), []byte(name), 0o600))
	}
	first, err := service.RemoveEntry(t.Context(), agentRemoval(project, "a", textfile.SHA256([]byte("a"))))
	testutil.FailErr(t, "first removal", err)
	reached, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	undoCtx := WithSourceEffect(t.Context(), func() error { close(reached); <-release; return nil })
	undone := make(chan error, 1)
	go func() {
		_, err := service.Undo(undoCtx, uuid.NewString(), project, SourceHistoryMutationRequest{ExpectedEntryID: first})
		undone <- err
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("Undo did not reach publication boundary")
	}
	removed := make(chan error, 1)
	go func() {
		_, err := service.RemoveEntry(t.Context(), agentRemoval(project, "b", textfile.SHA256([]byte("b"))))
		removed <- err
	}()
	select {
	case err := <-removed:
		t.Fatalf("new removal overtook unsettled Undo: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	testutil.FailErr(t, "settle Undo", <-undone)
	testutil.FailErr(t, "settle new removal", <-removed)
	state, err := service.History.State(t.Context(), project.ID)
	testutil.FailErr(t, "read ordered history", err)
	if state.Undo == nil || state.Undo.Path != "b" || state.Redo != nil {
		t.Fatalf("new history did not replace the undone branch: %+v", state)
	}
	assertSourceHistoryFile(t, root, "a", "a")
}

func TestMemoryRecoveryQuotaKeepsOtherProjectsActiveCapture(t *testing.T) {
	service := NewSourceMutationService(nil, nil)
	service.History.SetRecoveryQuotaBytes(4)
	type removalRun struct {
		project          *Project
		root, body       string
		reached, release chan struct{}
		unblock          func()
		done             chan error
	}
	var runs []removalRun
	for i, body := range []string{"aaaa", "bbbb"} {
		root := t.TempDir()
		id := fmt.Sprintf("project-%d", i)
		project := &Project{ID: id, Roots: []Root{{ID: id, ProjectID: id, Path: root, IsPrimary: true}}}
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte(body), 0o600))
		reached, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		var once sync.Once
		ctx := WithSourceProgress(t.Context(), func(progress SourceProgress) {
			if progress.Phase == "preserving" {
				once.Do(func() { close(reached); <-release })
			}
		})
		done := make(chan error, 1)
		go func() {
			_, err := service.RemoveEntry(ctx, agentRemoval(project, "file", textfile.SHA256([]byte(body))))
			done <- err
		}()
		runs = append(runs, removalRun{project: project, root: root, body: body, reached: reached, release: release, unblock: unblock, done: done})
	}
	for _, run := range runs {
		select {
		case <-run.reached:
		case <-time.After(5 * time.Second):
			t.Fatal("project did not begin capture")
		}
	}
	runs[0].unblock()
	testutil.FailErr(t, "settle first project while another captures", <-runs[0].done)
	runs[1].unblock()
	testutil.FailErr(t, "settle second project", <-runs[1].done)
	if len(service.recovery.recoveryBytes) != 2 {
		t.Fatalf("reclamation dropped captured bodies: %d", len(service.recovery.recoveryBytes))
	}
	for _, run := range runs {
		state, err := service.History.State(t.Context(), run.project.ID)
		testutil.FailErr(t, "read project Undo", err)
		if state.Undo == nil {
			t.Fatal("project lost Undo")
		}
		undoHistoryHead(t, service, run.project, state.Undo.ID)
		assertSourceHistoryFile(t, run.root, "file", run.body)
	}
}
