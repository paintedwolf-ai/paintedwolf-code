package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromoteTransactionRecoversShippedJournal(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	task := &api.WorkerTask{ID: "job-shipped", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	primary := filepath.Join(root, "a.txt")
	testutil.FailErr(t, "seed landed target", os.WriteFile(primary, []byte("new\n"), 0o644))
	testutil.FailErr(t, "chmod landed target", os.Chmod(primary, 0o644))
	txPath := shippedPromoteJournalPath(dataDir, task.ID)
	testutil.FailErr(t, "mkdir journals", os.MkdirAll(filepath.Dir(txPath), 0o700))
	// The exact bytes the released build writes for one edited file.
	shipped := fmt.Sprintf(`{"version":1,"job_id":"job-shipped","mutations":[{"path":"a.txt","original":"b2xkCg==","original_exists":true,"original_mode":420,"target_sha256":%q,"target_exists":true,"target_mode":420}]}`, contentSHA256([]byte("new\n")))
	testutil.FailErr(t, "write shipped journal", os.WriteFile(txPath, []byte(shipped), 0o600))

	promote := PromoteRootsForTask(task, nil)
	found, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task)
	testutil.FailErr(t, "recover shipped journal", err)
	if !found {
		t.Fatal("shipped journal not found")
	}
	raw, err := os.ReadFile(primary)
	testutil.FailErr(t, "read recovered", err)
	if string(raw) != "old\n" {
		t.Fatalf("recovered = %q", raw)
	}

	for name, body := range map[string]string{
		"unknown version":    `{"version":3,"job_id":"job-shipped","mutations":[]}`,
		"unreleased version": `{"version":2,"job_id":"job-shipped","phase":"applying","mutations":[]}`,
		"shipped with phase": `{"version":1,"job_id":"job-shipped","phase":"applying","mutations":[]}`,
		"unknown field":      `{"version":1,"job_id":"job-shipped","unknown":true,"mutations":[]}`,
		"foreign job":        `{"version":1,"job_id":"other","mutations":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			testutil.FailErr(t, "write journal", os.WriteFile(txPath, []byte(body), 0o600))
			if _, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task); err == nil {
				t.Fatal("unwritten journal shape accepted")
			}
			if raw, err := os.ReadFile(primary); err != nil || string(raw) != "old\n" {
				t.Fatalf("refusal mutated primary: %q %v", raw, err)
			}
		})
	}
}

func TestPromoteTransactionWithoutHardLinkSupport(t *testing.T) {
	unavailable := func(fseffect.HardLinkRequest) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }
	seed := func(t *testing.T) (*api.WorkerTask, string, []promoteMutation) {
		root := t.TempDir()
		task := &api.WorkerTask{ID: "job-volume", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
		testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
		primary := filepath.Join(root, "a.txt")
		testutil.FailErr(t, "seed primary", os.WriteFile(primary, []byte("old\n"), 0o640))
		testutil.FailErr(t, "chmod primary", os.Chmod(primary, 0o640))
		return task, primary, []promoteMutation{{path: "a.txt", original: []byte("old\n"), originalExists: true, originalMode: 0o640, target: []byte("new\n"), targetExists: true}}
	}
	settle := func(t *testing.T, dataDir string, task *api.WorkerTask, primary string, effects promoteTransactionEffects, plans []promoteMutation) {
		t.Helper()
		effects.Reached = func(step checkpoint.TransactionStep, _ string) error {
			if step == checkpoint.StepTargetMutated {
				panic(errPromoteCrash)
			}
			return nil
		}
		promote := PromoteRootsForTask(task, nil)
		tx, err := beginPromoteTransactionWith(dataDir, task.ID, plans, effects)
		testutil.FailErr(t, "begin", err)
		crashDuring(t, func() { _ = tx.apply(t.Context(), promote, task) })
		if entries, err := os.ReadDir(filepath.Join(promoteTransactionDir(dataDir, task.ID), "retained")); err == nil && len(entries) != 0 {
			t.Fatalf("retained an inode without link support: %v", entries)
		}
		if raw, err := os.ReadFile(primary); err != nil || string(raw) != "new\n" {
			t.Fatalf("mutation did not land: %q %v", raw, err)
		}
		found, err := recoverPromoteTransaction(t.Context(), dataDir, task.ID, promote, task)
		testutil.FailErr(t, "recover", err)
		if !found {
			t.Fatal("journal not found")
		}
		info, err := os.Stat(primary)
		testutil.FailErr(t, "stat recovered", err)
		if raw, err := os.ReadFile(primary); err != nil || string(raw) != "old\n" || info.Mode().Perm() != 0o640 {
			t.Fatalf("recovered = %q mode %o err %v", raw, info.Mode().Perm(), err)
		}
	}

	t.Run("single name restores from journal content", func(t *testing.T) {
		dataDir := t.TempDir()
		task, primary, plans := seed(t)
		settle(t, dataDir, task, primary, promoteTransactionEffects{RetainLink: unavailable}, plans)
	})
	t.Run("distinct filesystem", func(t *testing.T) {
		dataDir, err := os.MkdirTemp("/dev/shm", "promote-transaction-")
		if err != nil {
			t.Skip("no second filesystem available")
		}
		t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
		task, primary, plans := seed(t)
		if same, err := fspath.SameFilesystem(dataDir, primary); err != nil || same {
			t.Skip("no second filesystem available")
		}
		settle(t, dataDir, task, primary, promoteTransactionEffects{}, plans)
	})
	t.Run("aliased original refuses before mutation", func(t *testing.T) {
		dataDir := t.TempDir()
		task, primary, plans := seed(t)
		testutil.FailErr(t, "link alias", os.Link(primary, filepath.Join(t.TempDir(), "alias")))
		tx, err := beginPromoteTransactionWith(dataDir, task.ID, plans, promoteTransactionEffects{RetainLink: unavailable})
		testutil.FailErr(t, "begin", err)
		if err := tx.apply(t.Context(), PromoteRootsForTask(task, nil), task); !errors.Is(err, syscall.EXDEV) {
			t.Fatalf("apply = %v, want cross-device refusal", err)
		}
		if raw, err := os.ReadFile(primary); err != nil || string(raw) != "old\n" {
			t.Fatalf("refusal mutated primary: %q %v", raw, err)
		}
	})
	t.Run("other link failures are not a capability", func(t *testing.T) {
		dataDir := t.TempDir()
		task, primary, plans := seed(t)
		tx, err := beginPromoteTransactionWith(dataDir, task.ID, plans, promoteTransactionEffects{RetainLink: func(fseffect.HardLinkRequest) error { return syscall.EIO }})
		testutil.FailErr(t, "begin", err)
		if err := tx.apply(t.Context(), PromoteRootsForTask(task, nil), task); !errors.Is(err, syscall.EIO) {
			t.Fatalf("apply = %v, want EIO", err)
		}
		if raw, err := os.ReadFile(primary); err != nil || string(raw) != "old\n" {
			t.Fatalf("failure mutated primary: %q %v", raw, err)
		}
	})
}
