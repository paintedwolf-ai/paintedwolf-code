package worker

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var errPromoteCrash = errors.New("simulated crash")

// promoteCrashFixture exercises every plan shape: edit with an out-of-root alias
// and a held descriptor, delete, and create.
type promoteCrashFixture struct {
	dataDir string
	task    *api.WorkerTask
	promote PromoteRoots
	plans   []promoteMutation
	edited  string
	removed string
	created string
	alias   string
	originA os.FileInfo
	originB os.FileInfo
}

func newPromoteCrashFixture(t *testing.T) *promoteCrashFixture {
	t.Helper()
	root := t.TempDir()
	task := &api.WorkerTask{ID: "job-crash", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	f := &promoteCrashFixture{
		dataDir: t.TempDir(),
		task:    task,
		promote: PromoteRootsForTask(task, nil),
		edited:  filepath.Join(root, "edited.sh"),
		removed: filepath.Join(root, "removed.txt"),
		created: filepath.Join(root, "created.txt"),
		alias:   filepath.Join(t.TempDir(), "alias.sh"),
		plans: []promoteMutation{
			{path: "edited.sh", original: []byte("old-edit\n"), originalExists: true, originalMode: 0o750, target: []byte("new-edit\n"), targetExists: true},
			{path: "removed.txt", original: []byte("old-removed\n"), originalExists: true, originalMode: 0o640},
			{path: "created.txt", target: []byte("new-created\n"), targetExists: true},
		},
	}
	testutil.FailErr(t, "seed edited", os.WriteFile(f.edited, []byte("old-edit\n"), 0o750))
	testutil.FailErr(t, "chmod edited", os.Chmod(f.edited, 0o750))
	testutil.FailErr(t, "seed removed", os.WriteFile(f.removed, []byte("old-removed\n"), 0o640))
	testutil.FailErr(t, "chmod removed", os.Chmod(f.removed, 0o640))
	testutil.FailErr(t, "link alias", os.Link(f.edited, f.alias))
	held, err := os.Open(f.edited)
	testutil.FailErr(t, "hold descriptor", err)
	t.Cleanup(func() { _ = held.Close() })
	f.originA, err = held.Stat()
	testutil.FailErr(t, "stat held descriptor", err)
	f.originB, err = os.Stat(f.removed)
	testutil.FailErr(t, "stat removed", err)
	retained := promoteTransactionRetainedDir(f.txPath())
	testutil.FailErr(t, "mkdir stale slots", os.MkdirAll(retained, 0o700))
	testutil.FailErr(t, "seed stale slot", os.WriteFile(filepath.Join(retained, "slot-stale"), []byte("stale"), 0o600))
	return f
}

func (f *promoteCrashFixture) txPath() string { return promoteTransactionPath(f.dataDir, f.task.ID) }

func (f *promoteCrashFixture) slot(path string) string {
	return filepath.Join(promoteTransactionRetainedDir(f.txPath()), promoteRetainedSlot(path))
}

// run stops at the crashAt'th step and abandons the transaction; a negative
// crashAt runs to completion. observe sees the durable state at each step.
func (f *promoteCrashFixture) run(t *testing.T, crashAt int, observe func(promoteStep, string)) error {
	t.Helper()
	n := 0
	effects := promoteTransactionEffects{reached: func(step promoteStep, path string) error {
		if observe != nil {
			observe(step, path)
		}
		if n == crashAt {
			return errPromoteCrash
		}
		n++
		return nil
	}}
	tx, err := beginPromoteTransactionWith(f.dataDir, f.task.ID, f.plans, effects)
	if err != nil {
		return err
	}
	return tx.apply(t.Context(), f.promote, f.task)
}

func (f *promoteCrashFixture) assertOriginals(t *testing.T) {
	t.Helper()
	for path, want := range map[string]string{f.edited: "old-edit\n", f.removed: "old-removed\n"} {
		raw, err := os.ReadFile(path)
		testutil.FailErr(t, "read original", err)
		if string(raw) != want {
			t.Fatalf("%s = %q, want %q", filepath.Base(path), raw, want)
		}
	}
	edited, err := os.Stat(f.edited)
	testutil.FailErr(t, "stat edited", err)
	alias, err := os.Stat(f.alias)
	testutil.FailErr(t, "stat alias", err)
	removed, err := os.Stat(f.removed)
	testutil.FailErr(t, "stat removed", err)
	if !os.SameFile(edited, f.originA) || !os.SameFile(alias, f.originA) || !os.SameFile(removed, f.originB) {
		t.Fatal("recovery did not preserve original inode, alias and held descriptor identity")
	}
	if edited.Mode().Perm() != 0o750 || removed.Mode().Perm() != 0o640 {
		t.Fatalf("modes = %o %o", edited.Mode().Perm(), removed.Mode().Perm())
	}
	if _, err := os.Lstat(f.created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created path survived recovery: %v", err)
	}
}

func (f *promoteCrashFixture) primarySnapshot(t *testing.T) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, path := range []string{f.edited, f.removed, f.created} {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			snapshot[path] = "<absent>"
			continue
		}
		testutil.FailErr(t, "snapshot primary", err)
		snapshot[path] = string(raw)
	}
	return snapshot
}

func (f *promoteCrashFixture) assertSettlesAndRetries(t *testing.T) {
	t.Helper()
	for range 2 {
		found, err := recoverPromoteTransaction(f.dataDir, f.task.ID, f.promote, f.task)
		testutil.FailErr(t, "recover", err)
		if !found {
			t.Fatal("journal not found")
		}
		f.assertOriginals(t)
	}
	testutil.FailErr(t, "remove journal", removePromoteTransaction(f.txPath()))
	if _, err := os.Lstat(promoteTransactionRetainedDir(f.txPath())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained originals survived settlement: %v", err)
	}
	if err := f.run(t, -1, nil); err != nil {
		t.Fatalf("retry after settlement: %v", err)
	}
	if raw, err := os.ReadFile(f.created); err != nil || string(raw) != "new-created\n" {
		t.Fatalf("retry did not land: %q %v", raw, err)
	}
}

func TestPromoteTransactionCrashPointContract(t *testing.T) {
	type point struct {
		step promoteStep
		path string
	}
	var points []point
	discovery := newPromoteCrashFixture(t)
	if err := discovery.run(t, -1, func(step promoteStep, path string) {
		points = append(points, point{step, path})
		journal, found, err := readPromoteTransaction(discovery.txPath())
		testutil.FailErr(t, "read journal at step", err)
		if !found || journal.Version != promoteTransactionVersion {
			t.Fatalf("%s: journal found=%v version=%d", step, found, journal.Version)
		}
		// The write-ahead invariant: a primary changes only under a durable applying record.
		mutated := step == promoteStepPrimaryMutated
		if mutated != (journal.Phase == promotePhaseApplying) && step != promoteStepApplyingDurable {
			t.Fatalf("%s %s: phase %q", step, path, journal.Phase)
		}
		if step != promoteStepPrimaryMutated {
			discovery.assertOriginals(t)
		}
		if step == promoteStepApplyingDurable {
			if journal.Phase != promotePhaseApplying {
				t.Fatalf("applying step read phase %q", journal.Phase)
			}
			for _, mutation := range journal.Mutations {
				if mutation.OriginalExists {
					testutil.FailErr(t, "validate retained at applying", validateRetainedPromoteOriginal(discovery.txPath(), mutation))
				}
			}
		}
	}); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	wantPoints := []point{
		{promoteStepPreparedDurable, ""}, {promoteStepStaleCleared, ""},
		{promoteStepOriginalRetained, "edited.sh"}, {promoteStepOriginalRetained, "removed.txt"},
		{promoteStepApplyingDurable, ""},
		{promoteStepPrimaryMutated, "edited.sh"}, {promoteStepPrimaryMutated, "removed.txt"}, {promoteStepPrimaryMutated, "created.txt"},
	}
	if !slices.Equal(points, wantPoints) {
		t.Fatalf("crash points = %v, want %v", points, wantPoints)
	}

	for i, at := range points {
		name := string(at.step) + "/" + strings.TrimSuffix(at.path, filepath.Ext(at.path))
		started := at.step == promoteStepApplyingDurable || at.step == promoteStepPrimaryMutated
		crash := func(t *testing.T) *promoteCrashFixture {
			t.Helper()
			f := newPromoteCrashFixture(t)
			if err := f.run(t, i, nil); !errors.Is(err, errPromoteCrash) {
				t.Fatalf("crash at %s: %v", name, err)
			}
			return f
		}
		t.Run(name+"/settles", func(t *testing.T) { crash(t).assertSettlesAndRetries(t) })
		if !started {
			t.Run(name+"/slots-lost", func(t *testing.T) {
				f := crash(t)
				testutil.FailErr(t, "lose slots", os.RemoveAll(promoteTransactionRetainedDir(f.txPath())))
				f.assertSettlesAndRetries(t)
			})
			t.Run(name+"/slot-foreign", func(t *testing.T) {
				f := crash(t)
				testutil.FailErr(t, "mkdir slots", os.MkdirAll(promoteTransactionRetainedDir(f.txPath()), 0o700))
				testutil.FailErr(t, "clear slot", os.RemoveAll(f.slot("edited.sh")))
				testutil.FailErr(t, "foreign slot", os.WriteFile(f.slot("edited.sh"), []byte("old-edit\n"), 0o750))
				f.assertSettlesAndRetries(t)
			})
			continue
		}
		t.Run(name+"/slot-missing-refused", func(t *testing.T) {
			f := crash(t)
			before := f.primarySnapshot(t)
			testutil.FailErr(t, "remove slot", os.Remove(f.slot("edited.sh")))
			if _, err := recoverPromoteTransaction(f.dataDir, f.task.ID, f.promote, f.task); err == nil || !strings.Contains(err.Error(), "missing retained original") {
				t.Fatalf("recover = %v, want missing retained original", err)
			}
			if !maps.Equal(before, f.primarySnapshot(t)) {
				t.Fatal("refused recovery mutated a primary")
			}
		})
		t.Run(name+"/slot-replaced-refused", func(t *testing.T) {
			f := crash(t)
			before := f.primarySnapshot(t)
			testutil.FailErr(t, "remove slot", os.Remove(f.slot("removed.txt")))
			testutil.FailErr(t, "replace slot", os.WriteFile(f.slot("removed.txt"), []byte("old-removed\n"), 0o640))
			testutil.FailErr(t, "chmod replaced slot", os.Chmod(f.slot("removed.txt"), 0o640))
			if _, err := recoverPromoteTransaction(f.dataDir, f.task.ID, f.promote, f.task); err == nil || !strings.Contains(err.Error(), "diverged") {
				t.Fatalf("recover = %v, want diverged", err)
			}
			if !maps.Equal(before, f.primarySnapshot(t)) {
				t.Fatal("refused recovery mutated a primary")
			}
		})
		if at.step == promoteStepPrimaryMutated {
			// Rollback itself may crash: restore in rollback order, then recover afresh.
			t.Run(name+"/recovery-crash", func(t *testing.T) {
				f := crash(t)
				journal, _, err := readPromoteTransaction(f.txPath())
				testutil.FailErr(t, "read journal", err)
				last := journal.Mutations[0]
				verify := func(fseffect.Target) error { return nil }
				if err := restorePromoteOriginalMutation(f.txPath(), f.promote, f.task, last, verify); err != nil {
					t.Fatalf("partial rollback: %v", err)
				}
				f.assertSettlesAndRetries(t)
			})
			continue
		}
		// A rename not yet covered by its directory sync may be lost; the mutation that
		// would depend on it cannot have run because Replace had not returned.
		t.Run(name+"/phase-lost", func(t *testing.T) {
			f := crash(t)
			journal, _, err := readPromoteTransaction(f.txPath())
			testutil.FailErr(t, "read journal", err)
			journal.Phase = promotePhasePrepared
			for i := range journal.Mutations {
				journal.Mutations[i].OriginalRetention, journal.Mutations[i].OriginalIdentity = "", ""
			}
			testutil.FailErr(t, "lose phase", writePromoteTransaction(f.txPath(), journal))
			f.assertSettlesAndRetries(t)
		})
	}
}

func TestPromoteTransactionCloseLeavesNoApplyingJournalWithoutSlots(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission this fault relies on")
	}
	f := newPromoteCrashFixture(t)
	if err := f.run(t, -1, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	retained := promoteTransactionRetainedDir(f.txPath())
	testutil.FailErr(t, "protect slots", os.Chmod(retained, 0o500))
	t.Cleanup(func() { _ = os.Chmod(retained, 0o700) })
	if err := removePromoteTransaction(f.txPath()); err == nil {
		t.Fatal("expected slot removal failure to be reported")
	}
	if _, err := os.Lstat(f.txPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("applying journal outlived a partial close: %v", err)
	}
	testutil.FailErr(t, "unprotect slots", os.Chmod(retained, 0o700))
	found, err := recoverPromoteTransaction(f.dataDir, f.task.ID, f.promote, f.task)
	if err != nil || found {
		t.Fatalf("recover after partial close = %v, %v", found, err)
	}
	if _, err := os.Lstat(retained); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inert slots survived recovery: %v", err)
	}
}

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
