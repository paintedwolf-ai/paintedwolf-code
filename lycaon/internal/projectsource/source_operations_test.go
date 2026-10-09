package projectsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestSourceMoveKeepsDescendantEditsAndUsesNoContentSnapshot(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "create tree", os.Mkdir(filepath.Join(root, "tree"), 0o700))
	file, err := os.Create(filepath.Join(root, "tree", "large"))
	testutil.FailErr(t, "create sparse file", err)
	testutil.FailErr(t, "size sparse file", file.Truncate(3<<30))
	testutil.FailErr(t, "close sparse file", file.Close())
	id := uuid.NewString()
	_, err = service.Rename(t.Context(), id, p, SourceRenameRequest{RootID: p.Roots[0].ID, From: "tree", To: "moved"})
	testutil.FailErr(t, "move large tree", err)
	row, _, err := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "load receipt", err)
	if row.Plan.TreeSHA != "" || row.Plan.RecoveryCount != 0 {
		t.Fatal("native move captured directory contents")
	}
	testutil.FailErr(t, "edit moved tree", os.WriteFile(filepath.Join(root, "moved", "later.txt"), []byte("later"), 0o600))
	undoHistoryHead(t, service, p, id)
	assertSourceHistoryFile(t, root, "tree/later.txt", "later")
	redoHistoryHead(t, service, p, id)
	assertSourceHistoryFile(t, root, "moved/later.txt", "later")
}

func TestSourceRecoveryManifestPagesAndRoundTrips(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "create tree", os.Mkdir(filepath.Join(root, "tree"), 0o700))
	// The manifest spans multiple 256-entry pages.
	const entries = 300
	for i := 0; i < entries; i++ {
		testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(root, "tree", fmt.Sprintf("%04d", i)), []byte(fmt.Sprintf("entry %d", i)), 0o640))
	}
	id := uuid.NewString()
	testutil.FailErr(t, "trash tree", service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "tree", Recursive: true}))
	var count, receiptBytes int
	testutil.FailErr(t, "count manifest", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries WHERE recovery_id=?`, id).Scan(&count))
	testutil.FailErr(t, "measure receipt", service.Journal.db.QueryRowContext(t.Context(), `SELECT length(plan_json) FROM source_mutations WHERE id=?`, id).Scan(&receiptBytes))
	if count != entries+1 || receiptBytes > 4096 {
		t.Fatalf("manifest count=%d receipt bytes=%d", count, receiptBytes)
	}
	undoHistoryHead(t, service, p, id)
	for _, i := range []int{0, 255, 256, entries - 1} {
		assertSourceHistoryFile(t, root, fmt.Sprintf("tree/%04d", i), fmt.Sprintf("entry %d", i))
	}
}

func TestSourceRecoveryCancellationDoesNotReplayOnRestart(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "file"), []byte("retained"), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = WithSourceProgress(ctx, func(progress SourceProgress) {
		if progress.Phase == "preserving" {
			cancel()
		}
	})
	id := uuid.NewString()
	err := service.Delete(ctx, id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	restarted.Effects.SetTrashMover(func(context.Context, string) error { t.Error("startup retried canceled trash"); return nil })
	testutil.FailErr(t, "recover", restarted.Recover(t.Context()))
	assertSourceHistoryFile(t, root, "file", "retained")
	state, err := restarted.History(t.Context(), p.ID)
	testutil.FailErr(t, "history", err)
	if state.Undo != nil {
		t.Fatal("canceled preparation entered history")
	}
}

func TestSourceRecoveryAllowsUnrelatedSave(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	for _, name := range []string{"large", "other"} {
		testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, name), []byte("before"), 0o600))
	}
	started, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	ctx := WithSourceProgress(t.Context(), func(progress SourceProgress) {
		if progress.Phase == "preserving" {
			first.Do(func() { close(started); <-release })
		}
	})
	done := make(chan error, 1)
	go func() {
		done <- service.Delete(ctx, uuid.NewString(), p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "large"})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("capture did not start")
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	saved := make(chan error, 1)
	go func() {
		_, err := service.Write(t.Context(), uuid.NewString(), p, SourceWriteRequest{RootID: p.Roots[0].ID, Path: "other", Content: "after", Encoding: textfile.UTF8, BaseSHA256: textfile.SHA256([]byte("before"))})
		saved <- err
	}()
	select {
	case err := <-saved:
		testutil.FailErr(t, "unrelated save", err)
	case <-time.After(10 * time.Second):
		t.Fatal("unrelated save waited for recovery")
	}
	unblock()
	select {
	case err := <-done:
		testutil.FailErr(t, "finish capture", err)
	case <-time.After(10 * time.Second):
		t.Fatal("capture did not finish")
	}
}

func TestSourceCrossVolumeMoveUndoRedoPreservesEdits(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	service.Effects.sameFilesystem = func(string, string) (bool, error) { return false, nil }
	testutil.FailErr(t, "create source", os.Mkdir(filepath.Join(root, "tree"), 0o700))
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "tree", "file"), []byte("before"), 0o640))
	id := uuid.NewString()
	_, err := service.Rename(t.Context(), id, p, SourceRenameRequest{RootID: p.Roots[0].ID, From: "tree", To: "moved"})
	testutil.FailErr(t, "cross-volume move", err)
	testutil.FailErr(t, "edit moved file", os.WriteFile(filepath.Join(root, "moved", "file"), []byte("edited"), 0o640))
	undoHistoryHead(t, service, p, id)
	assertSourceHistoryFile(t, root, "tree/file", "edited")
	redoHistoryHead(t, service, p, id)
	assertSourceHistoryFile(t, root, "moved/file", "edited")
}

func TestSourceCrossVolumeMoveRecoversPublicationAndPartialCleanup(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprintf("published=%v", published), func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			service.Effects.sameFilesystem = func(string, string) (bool, error) { return false, nil }
			testutil.FailErr(t, "create source", os.Mkdir(filepath.Join(root, "tree"), 0o700))
			for _, name := range []string{"a", "b"} {
				testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "tree", name), []byte(name), 0o600))
			}
			interrupted := errors.New("simulated interruption before publication")
			ctx := WithSourceEffect(t.Context(), func() error { panic(interrupted) })
			id := uuid.NewString()
			func() {
				defer func() {
					if got, ok := recover().(error); !ok || !errors.Is(got, interrupted) {
						t.Fatalf("simulated crash: %v", got)
					}
				}()
				_, _ = service.Rename(ctx, id, p, SourceRenameRequest{RootID: p.Roots[0].ID, From: "tree", To: "moved"})
			}()
			row, _, err := service.Journal.load(t.Context(), id)
			testutil.FailErr(t, "load prepared move", err)
			plan := &row.Plan
			plan.DestinationIdentity, err = fspath.EntryIdentity(filepath.Join(plan.StageAbs, "entry"))
			testutil.FailErr(t, "identify staged destination", err)
			plan.HoldAbs = filepath.Join(root, ".paintedwolf-move-"+id)
			plan.EffectStarted, plan.HoldStarted = true, true
			testutil.FailErr(t, "retain original before simulated crash", os.Rename(plan.FromAbs, plan.HoldAbs))
			if published {
				testutil.FailErr(t, "publish before simulated crash", os.Rename(filepath.Join(plan.StageAbs, "entry"), plan.ToAbs))
				plan.MoveCleanupStarted = true
				testutil.FailErr(t, "partial original cleanup", os.Remove(filepath.Join(plan.HoldAbs, "a")))
			}
			row.Status = sourceMutationPrepared
			testutil.FailErr(t, "persist crash boundary", service.Journal.update(t.Context(), row))
			restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
			testutil.FailErr(t, "recover move", restarted.Recover(t.Context()))
			row, _, err = restarted.Journal.load(t.Context(), id)
			testutil.FailErr(t, "load recovered move", err)
			if row.Status != sourceMutationCommitted {
				t.Fatalf("recovery status=%s error=%s", row.Status, row.Error)
			}
			for _, name := range []string{"a", "b"} {
				assertSourceHistoryFile(t, root, "moved/"+name, name)
			}
			if sourceMutationPathExists(plan.HoldAbs) || sourceMutationPathExists(plan.StageAbs) {
				t.Fatal("recovery left private move data")
			}
		})
	}
}

func TestSourceEntryIdentitySurvivesMoveAndEditButRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "from"), filepath.Join(root, "to")
	testutil.FailErr(t, "seed entry", os.WriteFile(from, []byte("before"), 0o600))
	identity, err := fspath.EntryIdentity(from)
	testutil.FailErr(t, "read identity", err)
	testutil.FailErr(t, "rename entry", os.Rename(from, to))
	testutil.FailErr(t, "edit entry", os.WriteFile(to, []byte("after"), 0o600))
	testutil.FailErr(t, "preserved identity", requireSourceIdentity(to, identity))
	testutil.FailErr(t, "retain former entry", os.Rename(to, from))
	testutil.FailErr(t, "replace entry", os.WriteFile(to, []byte("replacement"), 0o600))
	if err := requireSourceIdentity(to, identity); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestSourceStageRetryClearsReadOnlyTree(t *testing.T) {
	stage := t.TempDir()
	entry := filepath.Join(stage, "entry")
	testutil.FailErr(t, "create staged directory", os.Mkdir(entry, 0o700))
	testutil.FailErr(t, "write staged file", os.WriteFile(filepath.Join(entry, "file"), []byte("partial"), 0o400))
	testutil.FailErr(t, "make staged directory read-only", os.Chmod(entry, 0o500))
	identity, err := fspath.EntryIdentity(entry)
	testutil.FailErr(t, "identify private stage", err)
	plan := &sourceMutationPlan{RootPath: stage, StageAbs: entry, StageIdentity: identity}
	testutil.FailErr(t, "clear partial stage", clearSourceStage(plan))
	if _, err := os.Lstat(entry); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage entry remains: %v", err)
	}
	if err := clearSourceStage(plan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent stage: %v", err)
	}
}

func TestSourceCopyPreservesReadOnlyTree(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	source, copied := filepath.Join(root, "source"), filepath.Join(root, "copied")
	testutil.FailErr(t, "create source directory", os.Mkdir(source, 0o700))
	testutil.FailErr(t, "write read-only source", os.WriteFile(filepath.Join(source, "file"), []byte("retained"), 0o400))
	testutil.FailErr(t, "make source directory read-only", os.Chmod(source, 0o500))
	t.Cleanup(func() {
		_ = os.Chmod(source, 0o700)
		_ = os.Chmod(copied, 0o700)
	})
	_, err := service.Copy(t.Context(), uuid.NewString(), p, SourceCopyRequest{RootID: p.Roots[0].ID, From: "source", To: "copied"})
	testutil.FailErr(t, "copy read-only tree", err)
	for _, directory := range []string{"source", "copied"} {
		assertSourceHistoryFile(t, root, directory+"/file", "retained")
		for _, name := range []string{directory, directory + "/file"} {
			info, err := os.Stat(filepath.Join(root, name))
			testutil.FailErr(t, "read copied mode", err)
			if info.Mode().Perm()&0o200 != 0 {
				t.Fatalf("read-only mode lost at %s: %v", name, info.Mode())
			}
		}
	}
}

func TestSourceCopyRecoversPublishedDirectoryMode(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	source, copied := filepath.Join(root, "source"), filepath.Join(root, "copied")
	testutil.FailErr(t, "create source directory", os.Mkdir(source, 0o700))
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(source, "file"), []byte("retained"), 0o400))
	testutil.FailErr(t, "make source read-only", os.Chmod(source, 0o500))
	t.Cleanup(func() { _ = os.Chmod(source, 0o700); _ = os.Chmod(copied, 0o700) })
	id := uuid.NewString()
	interrupted := errors.New("simulated publication interruption")
	func() {
		defer func() {
			if got, ok := recover().(error); !ok || !errors.Is(got, interrupted) {
				t.Fatalf("simulated crash: %v", got)
			}
		}()
		ctx := WithSourceEffect(t.Context(), func() error { panic(interrupted) })
		_, _ = service.Copy(ctx, id, p, SourceCopyRequest{RootID: p.Roots[0].ID, From: "source", To: "copied"})
	}()
	row, _, err := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "load publication intent", err)
	entry := filepath.Join(row.Plan.StageAbs, "entry")
	testutil.FailErr(t, "allow staged publication", os.Chmod(entry, 0o700))
	testutil.FailErr(t, "simulate publication before mode restoration", os.Rename(entry, copied))
	row.Plan.EffectStarted = true
	testutil.FailErr(t, "persist interrupted publication", service.Journal.update(t.Context(), row))
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	testutil.FailErr(t, "recover directory publication", restarted.Recover(t.Context()))
	info, err := os.Stat(copied)
	testutil.FailErr(t, "read restored mode", err)
	if info.Mode().Perm()&0o200 != 0 {
		t.Fatalf("publication mode was not restored: %v", info.Mode())
	}
	assertSourceHistoryFile(t, root, "copied/file", "retained")
	row, _, err = restarted.Journal.load(t.Context(), id)
	testutil.FailErr(t, "load completed publication", err)
	if row.Status != sourceMutationCommitted {
		t.Fatalf("publication status = %s: %s", row.Status, row.Error)
	}
}

func TestSourceHistoryRetryRejectsChangedHeadBeforeEffects(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "source"), []byte("kept"), 0o600))
	moveID := uuid.NewString()
	_, err := service.Rename(t.Context(), moveID, p, SourceRenameRequest{RootID: p.Roots[0].ID, From: "source", To: "moved"})
	testutil.FailErr(t, "move source", err)
	undoID := uuid.NewString()
	canceled := WithSourceEffect(t.Context(), func() error { return context.Canceled })
	_, err = service.Undo(canceled, undoID, p, SourceHistoryMutationRequest{ExpectedEntryID: moveID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel undo: %v", err)
	}
	_, err = service.Create(t.Context(), uuid.NewString(), p, SourceEntryCreateRequest{RootID: p.Roots[0].ID, Path: "later", Kind: SourceEntryFolder})
	testutil.FailErr(t, "advance history", err)
	_, err = service.Undo(t.Context(), undoID, p, SourceHistoryMutationRequest{ExpectedEntryID: moveID})
	if !errors.Is(err, ErrSourceHistoryChanged) {
		t.Fatalf("stale retry: %v", err)
	}
	assertSourceHistoryFile(t, root, "moved", "kept")
}
