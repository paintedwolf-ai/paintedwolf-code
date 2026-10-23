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
	removal := agentRemoval(project, "tree", "")
	removal.Recursive = true
	id, err := service.RemoveEntry(t.Context(), removal)
	testutil.FailErr(t, "remove tree", err)
	row, found, err := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "read journal", err)
	if !found || row.Plan.RetainedTree == nil || row.Plan.RecoveryCount != 1 {
		t.Fatalf("tree recovery plan: %+v", row)
	}
	location, err := service.recovery.treeLocation(id)
	testutil.FailErr(t, "locate retained tree", err)
	if err := requireSourceIdentity(filepath.Join(location.Root, location.Rel), identity); err != nil {
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
			removal := agentRemoval(project, "tree", "")
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
				if !errors.As(err, &unavailable) || row.Status != sourceMutationFailed || row.Plan.RetainedTree != nil || row.Plan.RecoveryID != "" || row.Plan.RecoveryCount != 0 {
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
		return os.WriteFile(filepath.Join(root, "tree", "file"), []byte("changed after admission"), 0o600)
	})
	removal := agentRemoval(project, "tree", "")
	removal.Recursive = true
	id, err := service.RemoveEntry(ctx, removal)
	if !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("changed tree was accepted: %v", err)
	}
	row, found, loadErr := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "load unperformed attempt", loadErr)
	if !found || row.Plan.RetainedTree != nil || row.Plan.RecoveryID != "" {
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
					_, measured, err := sourceTreeFingerprint(t.Context(), filepath.Join(root, name))
					testutil.FailErr(t, "measure allocated storage", err)
					if !measured.AllocationKnown || measured.AllocatedBytes == 0 {
						t.Fatal("allocation fact unavailable")
					}
					service.History.SetRecoveryQuotaBytes(2 * measured.AllocatedBytes)
				}
				removal := agentRemoval(project, name, "")
				removal.Recursive = true
				id, err := service.RemoveEntry(t.Context(), removal)
				testutil.FailErr(t, "remove tree", err)
				ids = append(ids, id)
			}
			for i, id := range ids {
				location, err := service.recovery.treeLocation(id)
				testutil.FailErr(t, "locate tree", err)
				_, err = os.Stat(filepath.Join(location.Root, location.Rel))
				if (i == 0 && !os.IsNotExist(err)) || (i > 0 && err != nil) {
					t.Fatalf("retained tree %d: %v", i, err)
				}
			}
			undoHistoryHead(t, service, project, ids[2])
			assertSourceHistoryFile(t, root, "tree-2/file", "ok")
		})
	}
}

func TestRetainedTreeUnavailableDoesNotCapturePerFile(t *testing.T) {
	service, project, root, _ := sourceMutationFixture(t)
	service.Effects.sameFilesystem = func(string, string) (bool, error) { return false, nil }
	testutil.FailErr(t, "seed tree", os.Mkdir(filepath.Join(root, "tree"), 0o750))
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "tree", "file"), []byte("ok"), 0o600))
	removal := agentRemoval(project, "tree", "")
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
