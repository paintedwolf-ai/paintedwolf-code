package privategit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	t.Run("prior_failure_with_missing_source_and_failing_write_hook_rejects_write_intent", func(t *testing.T) {
		t.Parallel()
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		backingRoot := t.TempDir()
		mgr := newTestManager(t, backingRoot)
		mgr.cfg.FailRepoStateWriteHook = func(_, _, _ string) error {
			return errors.New("persistent repo-state write failure")
		}
		core := newMountedViewWithManager(t, mgr, "view-prior-fail-hook", srcDir)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}

		// Initial read fails when publishing established record.
		if _, err := core.ReadFile(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrInvalidRepoStateRecord) {
			t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord on initial read, got %v", err)
		}
		// Remove source .git while FailRepoStateWriteHook is STILL failing on "failed" record persistence.
		testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))

		for _, op := range []struct {
			name string
			fn   func() error
		}{
			{
				name: "Mkdir(.git)",
				fn: func() error {
					_, err := core.Mkdir(qp(".git"), 0o755)
					return err
				},
			},
			{
				name: "WriteFile(.git/HEAD)",
				fn: func() error {
					_, err := core.WriteFile(qp(".git/HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
					return err
				},
			},
		} {
			err := op.fn()
			if !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrInvalidRepoStateRecord) || errors.Is(err, ErrNoSourceRepository) {
				t.Fatalf("%s: expected ErrGitPending + ErrInvalidRepoStateRecord (and never ErrNoSourceRepository), got %v", op.name, err)
			}
		}
	})

	t.Run("restart_immediately_after_established_publication_failure_preserves_intent", func(t *testing.T) {
		t.Parallel()
		// Case A: Source .git is removed immediately after publication failure, before any retry or failure-record write.
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		backingRoot := t.TempDir()
		mgr1 := newTestManager(t, backingRoot)
		mgr1.cfg.FailRepoStateWriteHook = func(_, _, status string) error {
			if status == string(repoStatusEstablished) {
				return errors.New("injected established record write failure")
			}
			return nil
		}
		core1 := newMountedViewWithManager(t, mgr1, "view-immediate-restart", srcDir)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}
		if _, err := core1.ReadFile(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrInvalidRepoStateRecord) {
			t.Fatalf("expected ErrGitPending + ErrInvalidRepoStateRecord on initial read, got %v", err)
		}
		if _, err := os.Stat(repoInitIntentPath(mgr1, "view-immediate-restart", ".")); err != nil {
			t.Fatalf("expected durable init intent marker to remain on disk after publication failure: %v", err)
		}

		// Remove source .git and restart immediately with a fresh Manager (before any retry on mgr1).
		testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))
		mgr2 := newTestManager(t, backingRoot)
		core2 := newMountedViewWithManager(t, mgr2, "view-immediate-restart", srcDir)

		if _, err := core2.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on Stat(.git/HEAD) across immediate restart with missing source, got %v", err)
		}
		if _, err := core2.Mkdir(qp(".git"), 0o755); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on Mkdir(.git) across immediate restart with missing source, got %v", err)
		}
		if _, err := core2.WriteFile(qp(".git/HEAD"), []byte("ref: refs/heads/main\n"), 0o644); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on WriteFile(.git/HEAD) across immediate restart with missing source, got %v", err)
		}

		// Case B: When source .git remains intact across immediate restart, a fresh Manager recovers cleanly and clears the intent marker.
		srcKeep := t.TempDir()
		gittest.Init(t, srcKeep)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcKeep, "p.txt"), []byte("v1\n"), 0o644))
		gittest.CommitAll(t, srcKeep, "initial")

		backingKeep := t.TempDir()
		mgrKeep1 := newTestManager(t, backingKeep)
		mgrKeep1.cfg.FailRepoStateWriteHook = func(_, _, _ string) error {
			return errors.New("injected transient established write failure")
		}
		coreKeep1 := newMountedViewWithManager(t, mgrKeep1, "view-recover-restart", srcKeep)
		if _, err := coreKeep1.ReadFile(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on transient write failure, got %v", err)
		}
		mgrKeep2 := newTestManager(t, backingKeep)
		coreKeep2 := newMountedViewWithManager(t, mgrKeep2, "view-recover-restart", srcKeep)
		if _, err := coreKeep2.ReadFile(qp(".git/HEAD")); err != nil {
			t.Fatalf("expected clean recovery across restart when source .git remains intact, got %v", err)
		}
		if _, err := os.Stat(repoInitIntentPath(mgrKeep2, "view-recover-restart", ".")); !os.IsNotExist(err) {
			t.Fatalf("expected init intent marker to be removed after successful established publication, stat err=%v", err)
		}
	})

	t.Run("unknown_stale_leases_and_missing_non_head_dependencies_fail_closed", func(t *testing.T) {
		t.Parallel()

		setupEstablishedRepo := func(t *testing.T, viewID string) (string, string, *Manager, *layeredfs.Core) {
			t.Helper()
			srcDir := t.TempDir()
			gittest.Init(t, srcDir)
			testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("tracked-blob-content\n"), 0o644))
			gittest.CommitAll(t, srcDir, "initial")
			gittest.Run(t, srcDir, "checkout", "-b", "side")
			testutil.FailErr(t, "write side.txt", os.WriteFile(filepath.Join(srcDir, "side.txt"), []byte("side-branch-blob\n"), 0o644))
			gittest.CommitAll(t, srcDir, "side commit")
			gittest.Run(t, srcDir, "checkout", "-")

			backingRoot := t.TempDir()
			mgr := newTestManager(t, backingRoot)
			core := newMountedViewWithManager(t, mgr, viewID, srcDir)
			qp := layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"}
			_, err := core.ReadFile(qp)
			testutil.FailErr(t, "establish private git", err)
			return srcDir, backingRoot, mgr, core
		}

		// 1. Unknown lease ID in durable record fails closed both in-process and across restart.
		{
			srcDir, backingRoot, mgr1, core1 := setupEstablishedRepo(t, "view-unknown-lease")
			recPath := durableRepoStatePath(mgr1, "view-unknown-lease", ".")
			raw, err := os.ReadFile(recPath)
			testutil.FailErr(t, "read durable record", err)
			var rec durableRepoRecord
			testutil.FailErr(t, "unmarshal durable record", json.Unmarshal(raw, &rec))
			rec.LeaseID = "lease-view-999999"
			updated, err := json.Marshal(rec)
			testutil.FailErr(t, "marshal tampered record", err)
			testutil.FailErr(t, "write tampered record", os.WriteFile(recPath, updated, 0o600))
			testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))

			qp := func(rel string) layeredfs.QualifiedPath {
				return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
			}
			if _, err := core1.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("in-process unknown lease: expected ErrGitPending + ErrCorruptObjectStore, got %v", err)
			}
			mgr2 := newTestManager(t, backingRoot)
			core2 := newMountedViewWithManager(t, mgr2, "view-unknown-lease", srcDir)
			if _, err := core2.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("restart unknown lease Stat: expected ErrGitPending + ErrCorruptObjectStore, got %v", err)
			}
			if _, err := core2.WriteFile(qp(".git/config"), []byte("[core]\n"), 0o644); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("restart unknown lease WriteFile: expected ErrGitPending + ErrCorruptObjectStore, got %v", err)
			}
		}

		// 2. Stale/released lease ID fails closed both in-process and across restart.
		{
			srcDir, backingRoot, mgr1, core1 := setupEstablishedRepo(t, "view-stale-lease")
			view1, err := getManagedView(mgr1, "view-stale-lease")
			testutil.FailErr(t, "getManagedView", err)
			rec, ok, err := readDurableRepoRecord(mgr1, view1, "view-stale-lease", ".")
			if err != nil || !ok {
				t.Fatalf("readDurableRepoRecord: ok=%v err=%v", ok, err)
			}
			testutil.FailErr(t, "ReleaseLease", mgr1.Store().ReleaseLease(rec.LeaseID))
			testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))

			qp := func(rel string) layeredfs.QualifiedPath {
				return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
			}
			if _, err := core1.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("in-process released lease: expected ErrGitPending + ErrCorruptObjectStore, got %v", err)
			}
			mgr2 := newTestManager(t, backingRoot)
			core2 := newMountedViewWithManager(t, mgr2, "view-stale-lease", srcDir)
			if _, err := core2.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("restart released lease Stat: expected ErrGitPending + ErrCorruptObjectStore, got %v", err)
			}
			if _, err := core2.WriteFile(qp(".git/config"), []byte("[core]\n"), 0o644); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("restart released lease WriteFile: expected ErrGitPending + ErrCorruptObjectStore, got %v", err)
			}
		}

		// 3. Missing non-HEAD dependencies (tracked blob, root tree, non-HEAD branch commit) fail closed while HEAD commit remains intact.
		for _, depSpec := range []struct {
			name string
			rev  string
		}{
			{name: "missing_tracked_blob", rev: "HEAD:p.txt"},
			{name: "missing_root_tree", rev: "HEAD^{tree}"},
			{name: "missing_non_head_branch_commit", rev: "refs/heads/side"},
		} {
			srcDir, backingRoot, mgr1, core1 := setupEstablishedRepo(t, "view-"+depSpec.name)
			oid := strings.TrimSpace(gittest.Run(t, srcDir, "rev-parse", depSpec.rev))
			headOID := strings.TrimSpace(gittest.Run(t, srcDir, "rev-parse", "HEAD"))
			if oid == "" || oid == headOID {
				t.Fatalf("%s: expected non-empty non-HEAD oid, got oid=%q head=%q", depSpec.name, oid, headOID)
			}

			// Delete only the target loose object from the retained store; keep HEAD commit and objects/ directory intact.
			mgr1.Store().mu.Lock()
			unlockStoreWriteBoundary(mgr1.Store())
			mgr1.Store().mu.Unlock()
			objPath := filepath.Join(mgr1.Store().ObjectsDir(), oid[:2], oid[2:])
			testutil.FailErr(t, "remove dependency object "+depSpec.name, os.Remove(objPath))
			testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))

			qp := func(rel string) layeredfs.QualifiedPath {
				return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
			}
			if _, err := core1.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("%s in-process Stat: expected ErrGitPending + ErrCorruptObjectStore, got %v", depSpec.name, err)
			}
			mgr2 := newTestManager(t, backingRoot)
			core2 := newMountedViewWithManager(t, mgr2, "view-"+depSpec.name, srcDir)
			if _, err := core2.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("%s restart Stat: expected ErrGitPending + ErrCorruptObjectStore, got %v", depSpec.name, err)
			}
			if _, err := core2.WriteFile(qp(".git/config"), []byte("[core]\n"), 0o644); !errors.Is(err, layeredfs.ErrGitPending) || !errors.Is(err, ErrCorruptObjectStore) {
				t.Fatalf("%s restart WriteFile: expected ErrGitPending + ErrCorruptObjectStore, got %v", depSpec.name, err)
			}
		}
	})

	t.Run("materialized_and_canonical_symlink_aliases_accepted_while_lookalikes_and_mounted_worktree_rejected", func(t *testing.T) {
		t.Parallel()
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		baseDir := canonicalCleanPath(t.TempDir())
		realBacking := filepath.Join(baseDir, "real-backing")
		aliasBacking := filepath.Join(baseDir, "alias-backing")
		testutil.FailErr(t, "mkdir realBacking", os.MkdirAll(realBacking, 0o755))
		testutil.FailErr(t, "symlink aliasBacking", os.Symlink(realBacking, aliasBacking))

		realWT := filepath.Join(baseDir, "real-wt")
		aliasWT := filepath.Join(baseDir, "alias-wt")
		testutil.FailErr(t, "mkdir realWT", os.MkdirAll(realWT, 0o755))
		testutil.FailErr(t, "symlink aliasWT", os.Symlink(realWT, aliasWT))

		mgr := newTestManager(t, aliasBacking)
		matView := &managedView{
			viewID:           "view-mat-alias",
			trunkDir:         srcDir,
			worktreeDir:      aliasWT,
			canonWorktreeDir: canonicalCleanPath(aliasWT),
			materialized:     true,
			kernelMounted:    false,
		}
		if !isAllowedPrivateGitLocation(mgr, matView, "view-mat-alias", ".", filepath.Join(realWT, ".git")) {
			t.Fatalf("expected canonical materialized worktree .git to be accepted for symlinked worktreeDir")
		}
		if !isAllowedPrivateGitLocation(mgr, matView, "view-mat-alias", ".", filepath.Join(aliasWT, ".git")) {
			t.Fatalf("expected lexical materialized worktree .git to be accepted")
		}

		// Prove that distinct lookalike /tmp vs /private/tmp paths without symlink identity are rejected.
		lookalikeView := &managedView{
			viewID:           "view-lookalike",
			trunkDir:         srcDir,
			worktreeDir:      "/private/tmp/m14-distinct-lookalike-wt",
			canonWorktreeDir: "/private/tmp/m14-distinct-lookalike-wt",
			materialized:     true,
			kernelMounted:    false,
		}
		if !isAllowedPrivateGitLocation(mgr, lookalikeView, "view-lookalike", ".", "/private/tmp/m14-distinct-lookalike-wt/.git") {
			t.Fatalf("expected configured worktree .git to be accepted")
		}
		if isAllowedPrivateGitLocation(mgr, lookalikeView, "view-lookalike", ".", "/tmp/m14-distinct-lookalike-wt/.git") {
			t.Fatalf("expected unverified /tmp lookalike path to be rejected when canonWorktreeDir is /private/tmp/...")
		}

		// For a kernel-mounted view, the mounted worktree's .git is excluded from allowed durable locations.
		mntView := &managedView{
			viewID:        "view-mnt-excl",
			trunkDir:      srcDir,
			worktreeDir:   realWT,
			materialized:  false,
			kernelMounted: true,
		}
		if isAllowedPrivateGitLocation(mgr, mntView, "view-mnt-excl", ".", filepath.Join(realWT, ".git")) {
			t.Fatalf("expected kernel-mounted worktree .git to be excluded from allowed durable locations")
		}
		if !isAllowedPrivateGitLocation(mgr, mntView, "view-mnt-excl", ".", filepath.Join(canonicalCleanPath(realBacking), "privategit-mgr", "views", "view-mnt-excl", "private-git", "root", ".git")) {
			t.Fatalf("expected canonical backing private-git location to be accepted for kernel-mounted view")
		}
		if !isAllowedPrivateGitLocation(mgr, mntView, "view-mnt-excl", ".", filepath.Join(aliasBacking, "privategit-mgr", "views", "view-mnt-excl", "private-git", "root", ".git")) {
			t.Fatalf("expected lexical backing private-git location to be accepted for kernel-mounted view")
		}
	})

	t.Run("persisted_leases_participate_in_gc_before_view_reattachment_and_fail_closed_on_invalid_lease_files", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		srcA := t.TempDir()
		gittest.Init(t, srcA)
		testutil.FailErr(t, "write a.txt", os.WriteFile(filepath.Join(srcA, "a.txt"), []byte("unique-view-a-blob\n"), 0o644))
		gittest.CommitAll(t, srcA, "commit A")
		headA := strings.TrimSpace(gittest.Run(t, srcA, "rev-parse", "HEAD"))

		srcB := t.TempDir()
		gittest.Init(t, srcB)
		testutil.FailErr(t, "write b.txt", os.WriteFile(filepath.Join(srcB, "b.txt"), []byte("unique-view-b-blob\n"), 0o644))
		gittest.CommitAll(t, srcB, "commit B")
		headB := strings.TrimSpace(gittest.Run(t, srcB, "rev-parse", "HEAD"))

		backingRoot := t.TempDir()
		mgr1 := newTestManager(t, backingRoot)
		core1A := newMountedViewWithManager(t, mgr1, "view-gc-a", srcA)
		core1B := newMountedViewWithManager(t, mgr1, "view-gc-b", srcB)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}
		_, err := core1A.ReadFile(qp(".git/HEAD"))
		testutil.FailErr(t, "establish view-gc-a", err)
		_, err = core1B.ReadFile(qp(".git/HEAD"))
		testutil.FailErr(t, "establish view-gc-b", err)

		// Remove source .git directories so recovery relies strictly on retained store + private git.
		testutil.FailErr(t, "remove srcA .git", os.RemoveAll(filepath.Join(srcA, ".git")))
		testutil.FailErr(t, "remove srcB .git", os.RemoveAll(filepath.Join(srcB, ".git")))

		// Reopen store via a fresh Manager BEFORE reattaching any view, and run GC immediately.
		mgr2 := newTestManager(t, backingRoot)
		prunedBeforeAttach, err := mgr2.Store().CollectGarbage(ctx)
		testutil.FailErr(t, "CollectGarbage before view reattachment", err)
		if prunedBeforeAttach != 0 {
			t.Fatalf("expected 0 pruned objects before view reattachment, got %d", prunedBeforeAttach)
		}

		// Reattach ONLY view-gc-a while view-gc-b remains unattached, then run GC again.
		core2A := newMountedViewWithManager(t, mgr2, "view-gc-a", srcA)
		if _, err := core2A.ReadFile(qp(".git/HEAD")); err != nil {
			t.Fatalf("expected view-gc-a to recover after pre-attachment GC, got %v", err)
		}
		prunedPartialAttach, err := mgr2.Store().CollectGarbage(ctx)
		testutil.FailErr(t, "CollectGarbage with only view-gc-a reattached", err)
		if prunedPartialAttach != 0 {
			t.Fatalf("expected 0 pruned objects while view-gc-b lease is persisted on disk, got %d", prunedPartialAttach)
		}

		// Now reattach view-gc-b and verify its history is still intact.
		core2B := newMountedViewWithManager(t, mgr2, "view-gc-b", srcB)
		if _, err := core2B.ReadFile(qp(".git/HEAD")); err != nil {
			t.Fatalf("expected view-gc-b to recover after partial-attachment GC, got %v", err)
		}
		if !mgr2.Store().HasObject(headA) || !mgr2.Store().HasObject(headB) {
			t.Fatalf("expected both headA (%s) and headB (%s) to remain in store", headA, headB)
		}

		// Verify invalid/malformed lease files in <storeRoot>/leases cause both CollectGarbage and store open to fail closed.
		leasesDir := filepath.Join(mgr2.Store().RootDir(), "leases")
		badLeasePath := filepath.Join(leasesDir, "lease-view-9999.json")
		for _, badPayload := range []string{
			`{"format_version":1,"id":"lease-view-9999"`,
			`{"format_version":99,"id":"lease-view-9999","kind":"view","holder":"v","oids":["` + headA + `"]}`,
			`{"format_version":1,"id":"lease-view-9999","kind":"view","holder":"v","oids":["` + headA + `"],"unknown":1}`,
			`{"format_version":1,"id":"lease-view-9999","kind":"view","holder":"v","oids":["` + headA + `"]}{"extra":1}`,
			`{"format_version":1,"id":"../escape","kind":"view","holder":"v","oids":["` + headA + `"]}`,
			`{"format_version":1,"id":"lease-view-9999","kind":"view","holder":"v","oids":["not-a-hex-oid"]}`,
			`{"format_version":1,"id":"lease-view-9999","kind":"view","holder":"v","packs":["../evil"]}`,
		} {
			testutil.FailErr(t, "write bad lease file", os.WriteFile(badLeasePath, []byte(badPayload), 0o600))
			if _, gcErr := mgr2.Store().CollectGarbage(ctx); !errors.Is(gcErr, ErrCorruptObjectStore) {
				t.Fatalf("expected ErrCorruptObjectStore from CollectGarbage on bad lease payload %q, got %v", badPayload, gcErr)
			}
			if _, openErr := NewRetainedObjectStore(mgr2.Store().RootDir()); !errors.Is(openErr, ErrCorruptObjectStore) {
				t.Fatalf("expected ErrCorruptObjectStore from NewRetainedObjectStore on bad lease payload %q, got %v", badPayload, openErr)
			}
			testutil.FailErr(t, "remove bad lease file", os.Remove(badLeasePath))
		}
	})

	t.Run("release_lease_propagates_remove_error_and_preserves_retention_across_restart", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write rel.txt", os.WriteFile(filepath.Join(srcDir, "rel.txt"), []byte("release-error-test-blob\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")
		headOID := strings.TrimSpace(gittest.Run(t, srcDir, "rev-parse", "HEAD"))

		backingRoot := t.TempDir()
		mgr1 := newTestManager(t, backingRoot)
		core1 := newMountedViewWithManager(t, mgr1, "view-rel-err", srcDir)
		qp := layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"}
		_, err := core1.ReadFile(qp)
		testutil.FailErr(t, "establish view-rel-err", err)

		view1, err := getManagedView(mgr1, "view-rel-err")
		testutil.FailErr(t, "getManagedView", err)
		rec, ok, err := readDurableRepoRecord(mgr1, view1, "view-rel-err", ".")
		if err != nil || !ok {
			t.Fatalf("readDurableRepoRecord: ok=%v err=%v", ok, err)
		}

		// Inject a persistence failure when removing the lease file.
		mgr1.Store().mu.Lock()
		mgr1.Store().removeLeaseRecordFn = func(_ string) error {
			return errors.New("injected lease file remove failure")
		}
		mgr1.Store().mu.Unlock()

		if rErr := mgr1.Store().ReleaseLease(rec.LeaseID); rErr == nil {
			t.Fatalf("expected ReleaseLease to propagate remove error, got nil")
		}
		if vErr := mgr1.ReleaseView("view-rel-err"); vErr == nil {
			t.Fatalf("expected ReleaseView to propagate ReleaseLease error, got nil")
		}

		// Because removal failed, the lease must remain active both in memory and across store reopen.
		prunedInMem, err := mgr1.Store().CollectGarbage(ctx)
		testutil.FailErr(t, "CollectGarbage after failed ReleaseLease", err)
		if prunedInMem != 0 || !mgr1.Store().HasObject(headOID) {
			t.Fatalf("expected objects to remain retained after failed ReleaseLease, pruned=%d hasHead=%v", prunedInMem, mgr1.Store().HasObject(headOID))
		}

		reopenedStore, err := NewRetainedObjectStore(mgr1.Store().RootDir())
		testutil.FailErr(t, "reopen store after failed ReleaseLease", err)
		prunedReopen, err := reopenedStore.CollectGarbage(ctx)
		testutil.FailErr(t, "CollectGarbage on reopened store", err)
		if prunedReopen != 0 || !reopenedStore.HasObject(headOID) {
			t.Fatalf("expected objects to remain retained across restart after failed ReleaseLease, pruned=%d hasHead=%v", prunedReopen, reopenedStore.HasObject(headOID))
		}

		// Restore removal function and release cleanly; objects should now be eligible for GC.
		mgr1.Store().mu.Lock()
		mgr1.Store().removeLeaseRecordFn = nil
		mgr1.Store().mu.Unlock()
		testutil.FailErr(t, "ReleaseLease after restoring removeFn", mgr1.Store().ReleaseLease(rec.LeaseID))
		prunedAfterRelease, err := mgr1.Store().CollectGarbage(ctx)
		testutil.FailErr(t, "CollectGarbage after successful ReleaseLease", err)
		if prunedAfterRelease == 0 {
			t.Fatalf("expected objects to be pruned after successful ReleaseLease")
		}
	})

	t.Run("established_repo_with_malformed_or_unreadable_refs_or_index_fails_closed", func(t *testing.T) {
		t.Parallel()
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}

		// Case 1: Malformed .git/index in established private repository fails closed and preserves evidence.
		{
			srcDir := t.TempDir()
			gittest.Init(t, srcDir)
			testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
			gittest.CommitAll(t, srcDir, "initial")

			backingRoot := t.TempDir()
			mgr1 := newTestManager(t, backingRoot)
			core1 := newMountedViewWithManager(t, mgr1, "view-corrupt-index", srcDir)
			_, err := core1.ReadFile(qp(".git/HEAD"))
			testutil.FailErr(t, "establish view-corrupt-index", err)

			view1, err := getManagedView(mgr1, "view-corrupt-index")
			testutil.FailErr(t, "getManagedView", err)
			rec, ok, err := readDurableRepoRecord(mgr1, view1, "view-corrupt-index", ".")
			if err != nil || !ok {
				t.Fatalf("readDurableRepoRecord: ok=%v err=%v", ok, err)
			}
			testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))

			idxPath := filepath.Join(rec.PrivateGitDir, "index")
			corruptIdx := []byte("DIRC-corrupt-index-payload")
			testutil.FailErr(t, "write corrupt index", os.WriteFile(idxPath, corruptIdx, 0o644))

			if _, sErr := core1.Stat(qp(".git/HEAD")); !errors.Is(sErr, layeredfs.ErrGitPending) || !errors.Is(sErr, ErrCorruptObjectStore) {
				t.Fatalf("in-process corrupt index Stat: expected ErrGitPending + ErrCorruptObjectStore, got %v", sErr)
			}
			mgr2 := newTestManager(t, backingRoot)
			core2 := newMountedViewWithManager(t, mgr2, "view-corrupt-index", srcDir)
			if _, sErr := core2.Stat(qp(".git/HEAD")); !errors.Is(sErr, layeredfs.ErrGitPending) || !errors.Is(sErr, ErrCorruptObjectStore) {
				t.Fatalf("restart corrupt index Stat: expected ErrGitPending + ErrCorruptObjectStore, got %v", sErr)
			}
			if _, wErr := core2.WriteFile(qp(".git/config"), []byte("[core]\n"), 0o644); !errors.Is(wErr, layeredfs.ErrGitPending) || !errors.Is(wErr, ErrCorruptObjectStore) {
				t.Fatalf("restart corrupt index WriteFile: expected ErrGitPending + ErrCorruptObjectStore, got %v", wErr)
			}
			gotIdx, rErr := os.ReadFile(idxPath)
			testutil.FailErr(t, "read preserved corrupt index", rErr)
			if string(gotIdx) != string(corruptIdx) {
				t.Fatalf("expected corrupt index evidence to be preserved, got %q", string(gotIdx))
			}
		}

		// Case 2: Malformed ref file under .git/refs/heads in established private repository fails closed.
		{
			srcDir := t.TempDir()
			gittest.Init(t, srcDir)
			testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("v1\n"), 0o644))
			gittest.CommitAll(t, srcDir, "initial")

			backingRoot := t.TempDir()
			mgr1 := newTestManager(t, backingRoot)
			core1 := newMountedViewWithManager(t, mgr1, "view-corrupt-ref", srcDir)
			_, err := core1.ReadFile(qp(".git/HEAD"))
			testutil.FailErr(t, "establish view-corrupt-ref", err)

			view1, err := getManagedView(mgr1, "view-corrupt-ref")
			testutil.FailErr(t, "getManagedView", err)
			rec, ok, err := readDurableRepoRecord(mgr1, view1, "view-corrupt-ref", ".")
			if err != nil || !ok {
				t.Fatalf("readDurableRepoRecord: ok=%v err=%v", ok, err)
			}
			testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))

			badRefPath := filepath.Join(rec.PrivateGitDir, "refs", "heads", "corrupt-branch")
			testutil.FailErr(t, "write corrupt ref", os.WriteFile(badRefPath, []byte("not-a-valid-oid\n"), 0o644))

			if _, sErr := core1.Stat(qp(".git/HEAD")); !errors.Is(sErr, layeredfs.ErrGitPending) || !errors.Is(sErr, ErrCorruptObjectStore) {
				t.Fatalf("in-process corrupt ref Stat: expected ErrGitPending + ErrCorruptObjectStore, got %v", sErr)
			}
			mgr2 := newTestManager(t, backingRoot)
			core2 := newMountedViewWithManager(t, mgr2, "view-corrupt-ref", srcDir)
			if _, wErr := core2.WriteFile(qp(".git/config"), []byte("[core]\n"), 0o644); !errors.Is(wErr, layeredfs.ErrGitPending) || !errors.Is(wErr, ErrCorruptObjectStore) {
				t.Fatalf("restart corrupt ref WriteFile: expected ErrGitPending + ErrCorruptObjectStore, got %v", wErr)
			}
		}
	})
}
