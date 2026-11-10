package privategit

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestDurableRepoRecordFailClosedAcrossRestart(t *testing.T) {
	t.Parallel()

	t.Run("malformed_and_unknown_records_fail_closed_and_remain_preserved", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name    string
			payload string
		}{
			{
				name:    "invalid_json",
				payload: `{"format_version":1,"repo_rel":".",`,
			},
			{
				name:    "unknown_field",
				payload: `{"format_version":1,"repo_rel":".","status":"no_source_repo","extra":true}`,
			},
			{
				name:    "unsupported_format_version",
				payload: `{"format_version":99,"repo_rel":".","status":"no_source_repo"}`,
			},
			{
				name:    "mismatched_repo_rel",
				payload: `{"format_version":1,"repo_rel":"other","status":"no_source_repo"}`,
			},
			{
				name:    "unknown_status",
				payload: `{"format_version":1,"repo_rel":".","status":"mystery"}`,
			},
			{
				name:    "established_missing_private_git_dir",
				payload: `{"format_version":1,"repo_rel":".","status":"established","lease_id":"lease-1"}`,
			},
			{
				name:    "trailing_tokens",
				payload: `{"format_version":1,"repo_rel":".","status":"no_source_repo"}{"trailing":1}`,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				srcDir := t.TempDir()
				gittest.Init(t, srcDir)
				testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
				gittest.CommitAll(t, srcDir, "initial")

				backingRoot := t.TempDir()
				mgr1 := newTestManager(t, backingRoot)
				core1 := newMountedViewWithManager(t, mgr1, "view-malformed", srcDir)
				qp := func(rel string) layeredfs.QualifiedPath {
					return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
				}
				_, err := core1.ReadFile(qp(".git/HEAD"))
				testutil.FailErr(t, "establish private git", err)

				// Remove source .git and write malformed record before simulating restart.
				testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))
				recPath := durableRepoStatePath(mgr1, "view-malformed", ".")
				testutil.FailErr(t, "write malformed record", os.WriteFile(recPath, []byte(tc.payload), 0o600))

				mgr2 := newTestManager(t, backingRoot)
				core2 := newMountedViewWithManager(t, mgr2, "view-malformed", srcDir)

				if _, sErr := core2.Stat(qp(".git/HEAD")); !errors.Is(sErr, layeredfs.ErrGitPending) || !errors.Is(sErr, ErrInvalidRepoStateRecord) {
					t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord on Stat, got %v", sErr)
				}
				if _, wErr := core2.WriteFile(qp(".git/HEAD"), []byte("ref: refs/heads/evil\n"), 0o644); !errors.Is(wErr, layeredfs.ErrGitPending) || !errors.Is(wErr, ErrInvalidRepoStateRecord) {
					t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord on WriteFile, got %v", wErr)
				}
				// Verify malformed record was preserved on disk without being overwritten.
				afterRaw, rErr := os.ReadFile(recPath)
				testutil.FailErr(t, "read preserved record", rErr)
				if string(afterRaw) != tc.payload {
					t.Fatalf("malformed record was mutated: got %q, want %q", string(afterRaw), tc.payload)
				}
			})
		}
	})

	t.Run("denied_read_fails_closed_across_restart", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("chmod 0000 read denial requires non-root POSIX process")
		}
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		backingRoot := t.TempDir()
		mgr1 := newTestManager(t, backingRoot)
		core1 := newMountedViewWithManager(t, mgr1, "view-denied", srcDir)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}
		_, err := core1.ReadFile(qp(".git/HEAD"))
		testutil.FailErr(t, "establish private git", err)

		testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))
		recPath := durableRepoStatePath(mgr1, "view-denied", ".")
		testutil.FailErr(t, "chmod 0000 record", os.Chmod(recPath, 0o000))
		t.Cleanup(func() { _ = os.Chmod(recPath, 0o600) })

		mgr2 := newTestManager(t, backingRoot)
		core2 := newMountedViewWithManager(t, mgr2, "view-denied", srcDir)

		if _, sErr := core2.Stat(qp(".git/HEAD")); !errors.Is(sErr, layeredfs.ErrGitPending) || !errors.Is(sErr, ErrInvalidRepoStateRecord) {
			t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord on denied Stat, got %v", sErr)
		}
		if _, mErr := core2.Mkdir(qp(".git"), 0o755); !errors.Is(mErr, layeredfs.ErrGitPending) || !errors.Is(mErr, ErrInvalidRepoStateRecord) {
			t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord on denied Mkdir(.git), got %v", mErr)
		}
	})

	t.Run("injected_publication_failure_prevents_readiness_and_no_source_fallback", func(t *testing.T) {
		t.Parallel()
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		backingRoot := t.TempDir()
		mgr := newTestManager(t, backingRoot)
		mgr.cfg.FailRepoStateWriteHook = func(_, _, _ string) error {
			return errors.New("injected repo-state write failure")
		}
		core := newMountedViewWithManager(t, mgr, "view-pub-fail", srcDir)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}

		// Established write failure must block readiness.
		if _, err := core.ReadFile(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrInvalidRepoStateRecord) {
			t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord when established record write fails, got %v", err)
		}
		// Remove source .git and clear hook: prior failure must prevent reclassification as no_source_repo.
		testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))
		mgr.cfg.FailRepoStateWriteHook = nil
		if _, err := core.Mkdir(qp(".git"), 0o755); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on Mkdir(.git) after failed established-state write, got %v", err)
		}

		// Non-repository write failure must also block fallback creation in Core.
		_, err := core.Mkdir(qp("sub"), 0o755)
		testutil.FailErr(t, "mkdir sub", err)
		mgr.cfg.FailRepoStateWriteHook = func(_, _, _ string) error {
			return errors.New("injected no_source_repo write failure")
		}
		if _, err := core.Mkdir(qp("sub/.git"), 0o755); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrInvalidRepoStateRecord) {
			t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord when no_source_repo record write fails, got %v", err)
		}
	})

	t.Run("removed_source_git_with_prior_private_history_and_absent_record_preserves_evidence", func(t *testing.T) {
		t.Parallel()
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		backingRoot := t.TempDir()
		mgr1 := newTestManager(t, backingRoot)
		core1 := newMountedViewWithManager(t, mgr1, "view-prior-ev", srcDir)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}
		headBytes, err := core1.ReadFile(qp(".git/HEAD"))
		testutil.FailErr(t, "establish private git", err)

		// Remove both source .git AND the durable record file, leaving only the established private .git on disk.
		testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))
		recPath := durableRepoStatePath(mgr1, "view-prior-ev", ".")
		testutil.FailErr(t, "remove durable record", os.Remove(recPath))

		// Simulate restart with a fresh Manager on the same backingRoot.
		mgr2 := newTestManager(t, backingRoot)
		core2 := newMountedViewWithManager(t, mgr2, "view-prior-ev", srcDir)

		// Must preserve existing private .git evidence and reconstruct established durable record rather than overwriting as no_source_repo.
		gotHead, rErr := core2.ReadFile(qp(".git/HEAD"))
		testutil.FailErr(t, "ReadFile(.git/HEAD) from preserved private evidence", rErr)
		if string(gotHead) != string(headBytes) {
			t.Fatalf("got HEAD %q, want %q", string(gotHead), string(headBytes))
		}
		view2, vErr := getManagedView(mgr2, "view-prior-ev")
		testutil.FailErr(t, "getManagedView mgr2", vErr)
		rec, ok, dErr := readDurableRepoRecord(mgr2, view2, "view-prior-ev", ".")
		if dErr != nil || !ok || rec.Status != repoStatusEstablished {
			t.Fatalf("expected reconstructed durable status %q, got rec=%+v ok=%v err=%v", repoStatusEstablished, rec, ok, dErr)
		}

		// And if the retained object store is subsequently corrupted across another restart with the record removed again, it must fail closed.
		testutil.FailErr(t, "remove durable record again", os.Remove(recPath))
		mgr2.Store().mu.Lock()
		unlockStoreWriteBoundary(mgr2.Store())
		mgr2.Store().mu.Unlock()
		testutil.FailErr(t, "remove store objectsDir", os.RemoveAll(mgr2.Store().ObjectsDir()))

		mgr3 := newTestManager(t, backingRoot)
		// Remove the newly created empty objectsDir on mgr3 to simulate store loss after restart.
		mgr3.Store().mu.Lock()
		unlockStoreWriteBoundary(mgr3.Store())
		mgr3.Store().mu.Unlock()
		testutil.FailErr(t, "remove mgr3 store objectsDir", os.RemoveAll(mgr3.Store().ObjectsDir()))
		core3 := newMountedViewWithManager(t, mgr3, "view-prior-ev", srcDir)
		if _, err := core3.WriteFile(qp(".git/config"), []byte("[core]\n"), 0o644); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on WriteFile(.git/config) when private evidence exists and store fails, got %v", err)
		}
	})
}
