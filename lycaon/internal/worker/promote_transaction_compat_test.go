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
	txPath := promoteTransactionPath(dataDir, task.ID)
	testutil.FailErr(t, "mkdir journals", os.MkdirAll(filepath.Dir(txPath), 0o700))
	// The exact bytes the released build writes for one edited file.
	shipped := fmt.Sprintf(`{"version":1,"job_id":"job-shipped","mutations":[{"path":"a.txt","original":"b2xkCg==","original_exists":true,"original_mode":420,"target_sha256":%q,"target_exists":true,"target_mode":420}]}`, contentSHA256([]byte("new\n")))
	testutil.FailErr(t, "write shipped journal", os.WriteFile(txPath, []byte(shipped), 0o600))

	promote := PromoteRootsForTask(task, nil)
	found, err := recoverPromoteTransaction(dataDir, task.ID, promote, task)
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
		"unknown version":      `{"version":3,"job_id":"job-shipped","phase":"applying","mutations":[]}`,
		"shipped with phase":   `{"version":1,"job_id":"job-shipped","phase":"applying","mutations":[]}`,
		"current without":      `{"version":2,"job_id":"job-shipped","mutations":[]}`,
		"unknown phase":        `{"version":2,"job_id":"job-shipped","phase":"committed","mutations":[]}`,
		"foreign job":          `{"version":2,"job_id":"other","phase":"prepared","mutations":[]}`,
		"prepared retention":   `{"version":2,"job_id":"job-shipped","phase":"prepared","mutations":[{"path":"a.txt","original_exists":true,"original_retention":"inode","original_identity":"1:2","target_exists":false}]}`,
		"applying no decision": `{"version":2,"job_id":"job-shipped","phase":"applying","mutations":[{"path":"a.txt","original_exists":true,"target_exists":false}]}`,
		"inode no identity":    `{"version":2,"job_id":"job-shipped","phase":"applying","mutations":[{"path":"a.txt","original_exists":true,"original_retention":"inode","target_exists":false}]}`,
		"content with slot":    `{"version":2,"job_id":"job-shipped","phase":"applying","mutations":[{"path":"a.txt","original_exists":true,"original_retention":"content","original_identity":"1:2","target_exists":false}]}`,
		"identity for absent":  `{"version":2,"job_id":"job-shipped","phase":"applying","mutations":[{"path":"a.txt","original_exists":false,"original_retention":"content","target_exists":false}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			testutil.FailErr(t, "write journal", os.WriteFile(txPath, []byte(body), 0o600))
			if _, err := recoverPromoteTransaction(dataDir, task.ID, promote, task); err == nil {
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
		effects.reached = func(step promoteStep, _ string) error {
			if step == promoteStepPrimaryMutated {
				return errPromoteCrash
			}
			return nil
		}
		promote := PromoteRootsForTask(task, nil)
		tx, err := beginPromoteTransactionWith(dataDir, task.ID, plans, effects)
		testutil.FailErr(t, "begin", err)
		if err := tx.apply(t.Context(), promote, task); !errors.Is(err, errPromoteCrash) {
			t.Fatalf("apply = %v, want crash after mutation", err)
		}
		journal, _, err := readPromoteTransaction(tx.path)
		testutil.FailErr(t, "read journal", err)
		if got := journal.Mutations[0]; got.OriginalRetention != promoteRetentionContent || got.OriginalIdentity != "" {
			t.Fatalf("durable retention = %q %q, want content", got.OriginalRetention, got.OriginalIdentity)
		}
		if raw, err := os.ReadFile(primary); err != nil || string(raw) != "new\n" {
			t.Fatalf("mutation did not land: %q %v", raw, err)
		}
		found, err := recoverPromoteTransaction(dataDir, task.ID, promote, task)
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
		settle(t, dataDir, task, primary, promoteTransactionEffects{hardLink: unavailable}, plans)
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
		tx, err := beginPromoteTransactionWith(dataDir, task.ID, plans, promoteTransactionEffects{hardLink: unavailable})
		testutil.FailErr(t, "begin", err)
		err = tx.apply(t.Context(), PromoteRootsForTask(task, nil), task)
		var unstarted *promoteTransactionUnstartedError
		if !errors.As(err, &unstarted) || !errors.Is(err, syscall.EXDEV) {
			t.Fatalf("apply = %v, want unstarted cross-device refusal", err)
		}
		if raw, err := os.ReadFile(primary); err != nil || string(raw) != "old\n" {
			t.Fatalf("refusal mutated primary: %q %v", raw, err)
		}
	})
	t.Run("other link failures are not a capability", func(t *testing.T) {
		dataDir := t.TempDir()
		task, primary, plans := seed(t)
		tx, err := beginPromoteTransactionWith(dataDir, task.ID, plans, promoteTransactionEffects{hardLink: func(fseffect.HardLinkRequest) error { return syscall.EIO }})
		testutil.FailErr(t, "begin", err)
		var unstarted *promoteTransactionUnstartedError
		if err := tx.apply(t.Context(), PromoteRootsForTask(task, nil), task); !errors.As(err, &unstarted) || !errors.Is(err, syscall.EIO) {
			t.Fatalf("apply = %v, want unstarted EIO", err)
		}
		if raw, err := os.ReadFile(primary); err != nil || string(raw) != "old\n" {
			t.Fatalf("failure mutated primary: %q %v", raw, err)
		}
	})
}
