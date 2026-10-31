package projectsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentTreeRemovalRenamesAndRestoresItsOriginalEntries(t *testing.T) {
	service, project, root, _ := sourceMutationFixture(t)
	refuseTrash(t, service)
	path := filepath.Join(root, "tree")
	testutil.FailErr(t, "seed empty directory", os.MkdirAll(filepath.Join(path, "empty"), 0o750))
	testutil.FailErr(t, "seed binary executable", os.WriteFile(filepath.Join(path, "file"), []byte{0, 255, 1}, 0o750))
	testutil.FailErr(t, "seed link", os.Symlink("file", filepath.Join(path, "link")))
	ledger := service.settlement.recorder.(*sourceledger.Store)
	tracked, err := ledger.TrackFile(t.Context(), sourceledger.TrackInput{ProjectID: project.ID, RootID: project.Roots[0].ID, Path: "tree/file", Content: []byte{0, 255, 1}})
	testutil.FailErr(t, "track child identity", err)
	identity, err := fspath.EntryIdentity(path)
	testutil.FailErr(t, "identify tree", err)
	removal := agentRemoval(t, project, "tree", "")
	removal.Recursive = true
	id, err := service.RemoveEntry(t.Context(), removal)
	testutil.FailErr(t, "remove tree", err)
	row, found, err := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "read journal", err)
	if !found || row.Plan.NativeTrash == nil || !nativeTrashReceiptRecorded(&row.Plan) {
		t.Fatalf("tree recovery plan: %+v", row)
	}
	if err := requireSourceIdentity(row.Plan.NativeTrash.Receipt.Path, identity); err != nil {
		t.Fatalf("delete copied rather than renamed the original tree: %v", err)
	}
	var captures int
	testutil.FailErr(t, "count recovery captures", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries WHERE recovery_id=?`, id).Scan(&captures))
	if captures != 0 {
		t.Fatalf("tree deletion captured %d per-file blobs", captures)
	}
	service = recoverWithMaintenance(t, service, ledger)
	undoHistoryHead(t, service, project, id)
	if err := requireSourceIdentity(path, identity); err != nil {
		t.Fatalf("Undo did not rename original entries back: %v", err)
	}
	child, err := ledger.History.ResolveHead(t.Context(), project.ID, sourcebranch.Trunk, project.Roots[0].ID, "tree/file")
	testutil.FailErr(t, "resolve restored child identity", err)
	if child.FileID != tracked.FileID || child.VersionID != tracked.VersionID {
		t.Fatalf("Undo forked child history: %+v, original %+v", child, tracked)
	}
	redoHistoryHead(t, service, project, id)
	undoHistoryHead(t, service, project, id)
	got, err := os.ReadFile(filepath.Join(path, "file"))
	testutil.FailErr(t, "read restored binary", err)
	info, err := os.Stat(filepath.Join(path, "empty"))
	testutil.FailErr(t, "inspect restored empty directory", err)
	link, err := os.Readlink(filepath.Join(path, "link"))
	testutil.FailErr(t, "read restored link", err)
	if string(got) != string([]byte{0, 255, 1}) || !info.IsDir() || info.Mode().Perm() != 0o750 || link != "file" {
		t.Fatalf("restored tree: bytes=%v empty=%v link=%q", got, info, link)
	}
}

func TestRetainedTreeRenameFailureDistinguishesPublication(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprint(published), func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			path := filepath.Join(root, "tree")
			testutil.FailErr(t, "seed tree", os.Mkdir(path, 0o700))
			testutil.FailErr(t, "seed content", os.WriteFile(filepath.Join(path, "file"), []byte("original"), 0o600))
			identity, err := fspath.EntryIdentity(path)
			testutil.FailErr(t, "identify original", err)
			service.Effects.relocate = func(from, to fseffect.Location, expected string) error {
				if published {
					if err := fseffect.RelocateGuarded(from, to, expected); err != nil {
						return err
					}
				}
				return syscall.EIO
			}
			removal := agentRemoval(t, project, "tree", "")
			removal.Recursive = true
			id, err := service.RemoveEntry(t.Context(), removal)
			row, found, loadErr := service.Journal.load(t.Context(), id)
			testutil.FailErr(t, "load rename attempt", loadErr)
			if !found || !row.Plan.EffectStarted || !row.Plan.DeleteStarted {
				t.Fatalf("rename attempt facts were lost: %+v", row)
			}
			if published {
				var applied *sourceeffect.AppliedError
				if !errors.As(err, &applied) || row.Status != sourceMutationCommitted {
					t.Fatalf("applied flush failure lost settlement: status=%s error=%v", row.Status, err)
				}
				service.Effects.relocate = fseffect.RelocateGuarded
				undoHistoryHead(t, service, project, id)
			} else {
				var unavailable *sourceeffect.RecoveryUnavailableError
				if !errors.As(err, &unavailable) || row.Status != sourceMutationFailed || nativeTrashReceiptRecorded(&row.Plan) || row.Plan.RecoveryID != "" || row.Plan.RecoveryCount != 0 {
					t.Fatalf("unperformed rename kept a recovery claim: plan=%+v status=%s error=%v", row.Plan, row.Status, err)
				}
				state, stateErr := service.History.State(t.Context(), project.ID)
				testutil.FailErr(t, "read history", stateErr)
				if state.Undo != nil {
					t.Fatal("unperformed rename invented Undo")
				}
			}
			if err := requireSourceIdentity(path, identity); err != nil {
				t.Fatalf("original identity lost: %v", err)
			}
			assertSourceHistoryFile(t, root, "tree/file", "original")
		})
	}
}

func TestRetainedTreeRevalidatesAfterEffectAdmission(t *testing.T) {
	service, project, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed tree", os.Mkdir(filepath.Join(root, "tree"), 0o700))
	testutil.FailErr(t, "seed content", os.WriteFile(filepath.Join(root, "tree", "file"), []byte("original"), 0o600))
	ctx := WithSourceEffect(t.Context(), func() error {
		if err := os.Rename(filepath.Join(root, "tree"), filepath.Join(root, "prior-tree")); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(root, "tree"), 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "tree", "file"), []byte("changed after admission"), 0o600)
	})
	removal := agentRemoval(t, project, "tree", "")
	removal.Recursive = true
	id, err := service.RemoveEntry(ctx, removal)
	if !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("changed tree was accepted: %v", err)
	}
	row, found, loadErr := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "load unperformed attempt", loadErr)
	if !found || nativeTrashReceiptRecorded(&row.Plan) || row.Plan.RecoveryID != "" {
		t.Fatalf("unperformed attempt kept custody claim: %+v", row)
	}
	assertSourceHistoryFile(t, root, "tree/file", "changed after admission")
}

func TestRetainedTreeQuotaPhysicallyPrunesEvictedEntries(t *testing.T) {
	for _, memory := range []bool{false, true} {
		t.Run(fmt.Sprint(memory), func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			if memory {
				service = NewSourceMutationService(nil, nil)
			}
			t.Cleanup(func() { testutil.FailErr(t, "close recovery", service.Close()) })
			var ids []string
			for i := range 3 {
				name := fmt.Sprintf("tree-%d", i)
				testutil.FailErr(t, "seed tree", os.Mkdir(filepath.Join(root, name), 0o750))
				testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, name, "file"), []byte("ok"), 0o600))
				if i == 0 {
					_, measured, err := measureRemovalMetadata(t.Context(), filepath.Join(root, name), testSourceOperationMemory(t))
					testutil.FailErr(t, "measure allocated storage", err)
					if !measured.AllocationKnown || measured.AllocatedBytes == 0 {
						t.Fatal("allocation fact unavailable")
					}
					service.History.SetRecoveryQuotaBytes(2 * measured.AllocatedBytes)
				}
				removal := agentRemoval(t, project, name, "")
				removal.Recursive = true
				id, err := service.RemoveEntry(t.Context(), removal)
				testutil.FailErr(t, "remove tree", err)
				ids = append(ids, id)
			}
			testutil.FailErr(t, "wait background reclamation", service.WaitReclamation(t.Context()))
			for i, id := range ids {
				slotPath := filepath.Join(settingsoverlay.RecoveryDir(root), recoverySlotName(id))
				_, err := os.Stat(slotPath)
				if (i == 0 && !os.IsNotExist(err)) || (i > 0 && err != nil) {
					t.Fatalf("retained tree %d: %v", i, err)
				}
			}
			undoHistoryHead(t, service, project, ids[2])
			assertSourceHistoryFile(t, root, "tree-2/file", "ok")
			undoHistoryHead(t, service, project, ids[1])
			assertSourceHistoryFile(t, root, "tree-1/file", "ok")
			_, err := service.Undo(t.Context(), "undo-reclaimed", project, SourceHistoryMutationRequest{ExpectedEntryID: ids[0]})
			if !errors.Is(err, ErrSourceTrashUnavailable) {
				t.Fatalf("expected ErrSourceTrashUnavailable for reclaimed entry, got %v", err)
			}
		})
	}
}

func TestMovedAsideReclamationByHistoryAgeAndAcrossRestart(t *testing.T) {
	service, project, root, _ := sourceMutationFixture(t)
	ledger := service.settlement.recorder.(*sourceledger.Store)
	t.Cleanup(func() { testutil.FailErr(t, "close recovery", service.Close()) })

	// 1. Remove an entry, then push it out of the 100-operation lifecycle history.
	testutil.FailErr(t, "seed aged file", os.WriteFile(filepath.Join(root, "aged.txt"), []byte("aged"), 0o600))
	agedID, err := service.RemoveEntry(t.Context(), agentRemoval(t, project, "aged.txt", ""))
	testutil.FailErr(t, "remove aged file", err)
	agedSlot := filepath.Join(settingsoverlay.RecoveryDir(root), recoverySlotName(agedID))
	if _, err := os.Stat(agedSlot); err != nil {
		t.Fatalf("expected aged slot to exist before eviction: %v", err)
	}
	for i := range sourceHistoryLimit {
		name := fmt.Sprintf("filler-%d.txt", i)
		_, err := service.Create(t.Context(), fmt.Sprintf("create-filler-%d", i), project, SourceEntryCreateRequest{
			RootID: project.Roots[0].ID,
			Path:   name,
			Kind:   SourceEntryFile,
		})
		testutil.FailErr(t, "create filler", err)
	}
	testutil.FailErr(t, "wait age reclamation", service.WaitReclamation(t.Context()))
	if _, err := os.Stat(agedSlot); !os.IsNotExist(err) {
		t.Fatalf("expected aged slot to be reclaimed after falling out of 100-op history, stat err=%v", err)
	}
	_, err = service.Undo(t.Context(), "undo-after-age-reclaim", project, SourceHistoryMutationRequest{ExpectedEntryID: agedID})
	if !errors.Is(err, ErrSourceTrashUnavailable) {
		t.Fatalf("expected ErrSourceTrashUnavailable after history-age reclamation, got %v", err)
	}

	// Verify history-age reclamation across a restart when a slot was not yet deleted before shutdown.
	testutil.FailErr(t, "seed aged-restart file", os.WriteFile(filepath.Join(root, "aged-restart.txt"), []byte("aged-restart"), 0o600))
	agedRestartID, err := service.RemoveEntry(t.Context(), agentRemoval(t, project, "aged-restart.txt", ""))
	testutil.FailErr(t, "remove aged-restart file", err)
	testutil.FailErr(t, "wait aged-restart measurement", service.WaitReclamation(t.Context()))
	agedRestartSlot := filepath.Join(settingsoverlay.RecoveryDir(root), recoverySlotName(agedRestartID))
	if _, err := os.Stat(agedRestartSlot); err != nil {
		t.Fatalf("expected aged-restart slot before history eviction: %v", err)
	}
	_, err = service.History.db.ExecContext(t.Context(), `DELETE FROM source_history_entries WHERE id=?`, agedRestartID)
	testutil.FailErr(t, "simulate history-age eviction before restart", err)

	// 2. Verify both history-age and budget reclamation survive restart and report unavailable on Undo.
	testutil.FailErr(t, "seed restart tree", os.Mkdir(filepath.Join(root, "restart-tree"), 0o750))
	testutil.FailErr(t, "seed restart file", os.WriteFile(filepath.Join(root, "restart-tree", "f.txt"), []byte("restart"), 0o600))
	restartID, err := service.RemoveEntry(t.Context(), func() sourceeffect.Removal {
		r := agentRemoval(t, project, "restart-tree", "")
		r.Recursive = true
		return r
	}())
	testutil.FailErr(t, "remove restart tree", err)
	testutil.FailErr(t, "wait initial measurement", service.WaitReclamation(t.Context()))
	restartSlot := filepath.Join(settingsoverlay.RecoveryDir(root), recoverySlotName(restartID))
	if _, err := os.Stat(restartSlot); err != nil {
		t.Fatalf("expected restart slot before quota tightening: %v", err)
	}
	testutil.FailErr(t, "close initial service before restart", service.Close())
	restarted := NewSourceMutationService(service.Journal.db, ledger)
	t.Cleanup(func() { testutil.FailErr(t, "close restarted service", restarted.Close()) })
	restarted.History.SetRecoveryQuotaBytes(1)
	testutil.FailErr(t, "recover restarted service", restarted.Recover(t.Context()))
	for label, slot := range map[string]string{"aged-restart": agedRestartSlot, "budget-restart": restartSlot} {
		if _, err := os.Stat(slot); !os.IsNotExist(err) {
			t.Fatalf("expected %s slot to be reclaimed on Recover, stat err=%v", label, err)
		}
	}
	for label, id := range map[string]string{"aged-restart": agedRestartID, "budget-restart": restartID} {
		_, err = restarted.Undo(t.Context(), "undo-"+label, project, SourceHistoryMutationRequest{ExpectedEntryID: id})
		if !errors.Is(err, ErrSourceTrashUnavailable) {
			t.Fatalf("expected ErrSourceTrashUnavailable for %s after restart reclamation, got %v", label, err)
		}
	}

	// 3. Directory names never affect reclamation order: node_modules and src are treated identically (oldest first).
	for _, order := range [][2]string{{"src", "node_modules"}, {"node_modules", "src"}} {
		t.Run(order[0]+"_then_"+order[1], func(t *testing.T) {
			svc, proj, dirRoot, _ := sourceMutationFixture(t)
			t.Cleanup(func() { testutil.FailErr(t, "close dir-order service", svc.Close()) })
			for _, name := range order {
				testutil.FailErr(t, "seed "+name, os.Mkdir(filepath.Join(dirRoot, name), 0o750))
				testutil.FailErr(t, "seed "+name+" file", os.WriteFile(filepath.Join(dirRoot, name, "index.js"), []byte("same-bytes"), 0o600))
			}
			_, measured, err := measureRemovalMetadata(t.Context(), filepath.Join(dirRoot, order[0]), testSourceOperationMemory(t))
			testutil.FailErr(t, "measure dir allocation", err)
			svc.History.SetRecoveryQuotaBytes(measured.AllocatedBytes)

			var ids [2]string
			for i, name := range order {
				r := agentRemoval(t, proj, name, "")
				r.Recursive = true
				id, err := svc.RemoveEntry(t.Context(), r)
				testutil.FailErr(t, "remove "+name, err)
				ids[i] = id
			}
			testutil.FailErr(t, "wait reclamation", svc.WaitReclamation(t.Context()))
			olderSlot := filepath.Join(settingsoverlay.RecoveryDir(dirRoot), recoverySlotName(ids[0]))
			newerSlot := filepath.Join(settingsoverlay.RecoveryDir(dirRoot), recoverySlotName(ids[1]))
			if _, err := os.Stat(olderSlot); !os.IsNotExist(err) {
				t.Fatalf("older directory %q should be reclaimed first regardless of name, stat err=%v", order[0], err)
			}
			if _, err := os.Stat(newerSlot); err != nil {
				t.Fatalf("newer directory %q should remain retained regardless of name, stat err=%v", order[1], err)
			}
			undoHistoryHead(t, svc, proj, ids[1])
			assertSourceHistoryFile(t, dirRoot, filepath.Join(order[1], "index.js"), "same-bytes")
			_, err = svc.Undo(t.Context(), "undo-older-"+order[0], proj, SourceHistoryMutationRequest{ExpectedEntryID: ids[0]})
			if !errors.Is(err, ErrSourceTrashUnavailable) {
				t.Fatalf("expected ErrSourceTrashUnavailable for reclaimed older directory %q, got %v", order[0], err)
			}
		})
	}
}

func TestRetainedTreeUnavailableDoesNotCapturePerFile(t *testing.T) {
	service, project, root, _ := sourceMutationFixture(t)
	service.Effects.sameFilesystem = func(string, string) (bool, error) { return false, nil }
	testutil.FailErr(t, "seed tree", os.Mkdir(filepath.Join(root, "tree"), 0o750))
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "tree", "file"), []byte("ok"), 0o600))
	removal := agentRemoval(t, project, "tree", "")
	removal.Recursive = true
	_, err := service.RemoveEntry(t.Context(), removal)
	var unavailable *sourceeffect.RecoveryUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("cross-filesystem removal: %v", err)
	}
	assertSourceHistoryFile(t, root, "tree/file", "ok")
	var captures int
	testutil.FailErr(t, "count captures", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries`).Scan(&captures))
	if captures != 0 {
		t.Fatalf("rename-unavailable path captured %d entries", captures)
	}
}
