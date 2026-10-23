package projectsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestMemoryRecoveryQuotaPrunesActualCapturedContent(t *testing.T) {
	service := NewSourceMutationService(nil, nil)
	root := t.TempDir()
	project := &Project{ID: "project", Roots: []Root{{ID: "root", ProjectID: "project", Path: root, IsPrimary: true}}}
	service.History.SetRecoveryQuotaBytes(4)
	var latest string
	for i, body := range []string{"aa", "bb", "cc"} {
		name := fmt.Sprintf("file-%d", i)
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, name), []byte(body), 0o600))
		id, err := service.RemoveEntry(t.Context(), agentRemoval(t, project, name, textfile.SHA256([]byte(body))))
		testutil.FailErr(t, "remove", err)
		latest = id
	}
	if len(service.History.history) != 2 || len(service.recovery.recoveryEntries) != 2 || len(service.recovery.recoveryBytes) != 2 {
		t.Fatalf("unbounded retained state: history=%d manifests=%d bodies=%d", len(service.History.history), len(service.recovery.recoveryEntries), len(service.recovery.recoveryBytes))
	}
	_, err := service.Undo(t.Context(), uuid.NewString(), project, SourceHistoryMutationRequest{ExpectedEntryID: latest})
	testutil.FailErr(t, "undo retained entry", err)
	assertSourceHistoryFile(t, root, "file-2", "cc")
}

func TestRecoveryAdmissionRejectsOversizeBeforeEffect(t *testing.T) {
	for _, memory := range []bool{false, true} {
		t.Run(fmt.Sprint(memory), func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			if memory {
				service = NewSourceMutationService(nil, nil)
			}
			service.History.SetRecoveryQuotaBytes(2)
			testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte("body"), 0o600))
			_, err := service.RemoveEntry(t.Context(), agentRemoval(t, project, "file", textfile.SHA256([]byte("body"))))
			if !errors.Is(err, ErrSourceRecoveryQuota) {
				t.Fatalf("oversize capture = %v", err)
			}
			assertSourceHistoryFile(t, root, "file", "body")
			state, err := service.History.State(t.Context(), project.ID)
			testutil.FailErr(t, "read history", err)
			if state.Undo != nil {
				t.Fatalf("failed admission created history: %+v", state)
			}
		})
	}
}

func TestMemoryRecoveryQuotaConcurrentPublication(t *testing.T) {
	service := NewSourceMutationService(nil, nil)
	root := t.TempDir()
	project := &Project{ID: "project", Roots: []Root{{ID: "root", ProjectID: "project", Path: root, IsPrimary: true}}}
	service.History.SetRecoveryQuotaBytes(4)
	var wg sync.WaitGroup
	failures := make(chan error, 3)
	for i, body := range []string{"aa", "bb", "cc"} {
		name := fmt.Sprintf("file-%d", i)
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, name), []byte(body), 0o600))
		wg.Go(func() {
			_, err := service.RemoveEntry(t.Context(), agentRemoval(t, project, name, textfile.SHA256([]byte(body))))
			failures <- err
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		testutil.FailErr(t, "concurrent removal", err)
	}
	var bytes int
	for _, body := range service.recovery.recoveryBytes {
		bytes += len(body)
	}
	if bytes > 4 || len(service.History.history) != 2 {
		t.Fatalf("retained bytes=%d history=%d", bytes, len(service.History.history))
	}
}

func TestRecoveryQuotaNonpositiveRestoresDefault(t *testing.T) {
	service := NewSourceMutationService(nil, nil)
	for _, quota := range []int64{0, -1, 8} {
		service.History.SetRecoveryQuotaBytes(quota)
		want := int64(DefaultSourceHistoryRecoveryQuotaBytes)
		if quota > 0 {
			want = quota
		}
		if got := service.RecoveryQuotaBytes(); got != want {
			t.Fatalf("quota(%d)=%d; want %d", quota, got, want)
		}
	}
}

func TestPermanentRemovalRevalidatesPreparedContentBeforeDisposal(t *testing.T) {
	service, project, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed", os.Mkdir(filepath.Join(root, "tree"), 0o700))
	testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(root, "tree", "file"), []byte("before"), 0o600))
	removal := agentRemoval(t, project, "tree", "")
	removal.Recursive, removal.Unrecoverable = true, true
	snapshot, err := service.InspectRemoval(t.Context(), fseffect.Location{Root: root, Rel: "tree"}, testSourceOperationMemory(t))
	testutil.FailErr(t, "measure reviewed snapshot", err)
	removal.ReviewedSnapshot = &snapshot
	ctx := WithSourceEffect(t.Context(), func() error {
		return os.WriteFile(filepath.Join(root, "tree", "file"), []byte("grew after preparation"), 0o600)
	})
	_, err = service.RemoveEntry(ctx, removal)
	if !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("changed prepared tree = %v", err)
	}
	assertSourceHistoryFile(t, root, "tree/file", "grew after preparation")
}

func TestQuotaChangesWaitForAcceptedRemoval(t *testing.T) {
	service := NewSourceMutationService(nil, nil)
	root := t.TempDir()
	project := &Project{ID: "project", Roots: []Root{{ID: "root", ProjectID: "project", Path: root, IsPrimary: true}}}
	service.History.SetRecoveryQuotaBytes(4)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte("body"), 0o600))
	reached, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	ctx := WithSourceEffect(t.Context(), func() error { close(reached); <-release; return nil })
	removed := make(chan error, 1)
	go func() {
		_, err := service.RemoveEntry(ctx, agentRemoval(t, project, "file", textfile.SHA256([]byte("body"))))
		removed <- err
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("removal did not reach effect boundary")
	}
	changing, changed := make(chan struct{}), make(chan struct{})
	go func() { close(changing); service.History.SetRecoveryQuotaBytes(1); close(changed) }()
	<-changing
	select {
	case <-changed:
		t.Fatal("quota changed before accepted removal finished")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	testutil.FailErr(t, "settle removal", <-removed)
	<-changed
	state, err := service.History.State(t.Context(), project.ID)
	testutil.FailErr(t, "read accepted Undo", err)
	if state.Undo == nil {
		t.Fatal("quota update discarded the accepted removal's Undo")
	}
}

func TestAppliedRecoveryKeepsUndoWhenRestartQuotaIsLower(t *testing.T) {
	service, project, root, _ := sourceMutationFixture(t)
	ledger := service.settlement.recorder.(*sourceledger.Store)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte("body"), 0o600))
	removal := agentRemoval(t, project, "file", textfile.SHA256([]byte("body")))
	removal.Record.OperationID = uuid.NewString()
	interrupted := errors.New("pause before effect")
	func() {
		defer func() {
			if recovered := recover(); recovered != interrupted {
				t.Fatalf("interruption=%v", recovered)
			}
		}()
		_, _ = service.RemoveEntry(WithSourceEffect(t.Context(), func() error { panic(interrupted) }), removal)
	}()
	row, found, err := service.Journal.load(t.Context(), removal.Record.OperationID)
	testutil.FailErr(t, "read prepared receipt", err)
	if !found {
		t.Fatal("prepared receipt missing")
	}
	row.memory = removal.Memory
	testutil.FailErr(t, "apply without settlement", service.Effects.applyMutation(t.Context(), row))
	row.Status = sourceMutationFileApplied
	testutil.FailErr(t, "record interrupted settlement", service.Journal.update(t.Context(), row))
	restarted := NewSourceMutationService(service.Journal.db, ledger)
	restarted.History.SetRecoveryQuotaBytes(1)
	testutil.FailErr(t, "settle accepted recovery", restarted.Recover(t.Context()))
	state, err := restarted.History.State(t.Context(), project.ID)
	testutil.FailErr(t, "read recovered Undo", err)
	if state.Undo == nil || state.Undo.ID != removal.Record.OperationID {
		t.Fatalf("accepted recovery lost Undo: %+v", state)
	}
	undoHistoryHead(t, restarted, project, removal.Record.OperationID)
	assertSourceHistoryFile(t, root, "file", "body")
}
