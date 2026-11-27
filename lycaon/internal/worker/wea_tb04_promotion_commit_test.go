package worker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// tb04RejectFormatter constructs the stock static reject formatter for TB04 tests.
func tb04RejectFormatter(t *testing.T) *guidance.StaticRejectFormatter {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return guidance.NewStaticRejectFormatter(cfg)
}

// tb04HashTree independently hashes every regular file under root (excluding .lycaon/.git metadata)
// so tests verify primary-tree atomicity without relying on product code.
func tb04HashTree(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".lycaon" || name == ".paintedwolf" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		entries = append(entries, filepath.ToSlash(rel)+"="+hex.EncodeToString(sum[:]))
		return nil
	})
	testutil.FailErr(t, "tb04HashTree", err)
	sort.Strings(entries)
	h := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(h[:])
}

// tb04WriteFiles writes a map of relative path -> bytes under root.
func tb04WriteFiles(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(abs, body, 0o644))
	}
}

// tb04CopyDirFiles copies all non-hidden regular files from src to dst.
func tb04CopyDirFiles(t *testing.T, src, dst string) {
	t.Helper()
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(dst, 0o755))
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".lycaon" || name == ".paintedwolf" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, raw, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
	testutil.FailErr(t, "tb04CopyDirFiles", err)
}

// tb04SQLFixture wires a real SQLite DB, SQLServices (SQLQueue + PromotionStore),
// and MergeService against a temporary primary workspace.
type tb04SQLFixture struct {
	dbPath  string
	sqlDB   *db.Store
	dataDir string
	primary string
	parent  string
	svcs    SQLServices
	merge   *MergeService
}

func tb04NewSQLFixture(t *testing.T, initialFiles map[string][]byte) *tb04SQLFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	dataDir := testbaseline.DataDir(t, sqlDB)
	dbPath := filepath.Join(dataDir, "store.db")
	primary := t.TempDir()
	tb04WriteFiles(t, primary, initialFiles)
	parent := "tb04-parent"
	testdbseed.InsertSessionWithRoot(t, sqlDB, parent, testdbseed.DefaultProjectID, primary)

	svcs := NewSQLServices(sqlDB, 8)
	svcs.Queue.Branches.SetBaselineStore(
		workspacebaseline.New(sqlDB, sourceblob.New(filepath.Join(dataDir, "source-content")), filepath.Join(dataDir, "worker-baselines")),
	)
	merge := &MergeService{
		Queue:    svcs.Queue,
		Branches: svcs.Queue.Branches,
		Store:    svcs.Promotions,
		Reject:   tb04RejectFormatter(t),
		DataDir:  dataDir,
	}
	return &tb04SQLFixture{
		dbPath:  dbPath,
		sqlDB:   sqlDB,
		dataDir: dataDir,
		primary: primary,
		parent:  parent,
		svcs:    svcs,
		merge:   merge,
	}
}

// tb04Reopen closes the current SQLite handle and opens a fresh one on the same DB file
// to simulate a full process restart with fresh SQLServices and MergeService.
func (f *tb04SQLFixture) reopen(t *testing.T) {
	t.Helper()
	testutil.FailErr(t, "close db before restart", f.sqlDB.Close())
	f.sqlDB = testdbfixture.OpenPath(t, f.dbPath)
	f.svcs = NewSQLServices(f.sqlDB, 8)
	f.svcs.Queue.Branches.SetBaselineStore(
		workspacebaseline.New(f.sqlDB, sourceblob.New(filepath.Join(f.dataDir, "source-content")), filepath.Join(f.dataDir, "worker-baselines")),
	)
	f.merge = &MergeService{
		Queue:    f.svcs.Queue,
		Branches: f.svcs.Queue.Branches,
		Store:    f.svcs.Promotions,
		Reject:   tb04RejectFormatter(t),
		DataDir:  f.dataDir,
	}
}

// tb04SeedPendingWorker enqueues a write worker, captures a durable baseline of primary,
// materializes a branch copy under dataDir/worker-branches/<jobID>, applies branchEdits/deletes,
// and marks the job complete+pending so it is ready for MergeService.PromoteOverlay.
func (f *tb04SQLFixture) seedPendingWorker(
	t *testing.T,
	jobID string,
	scopePaths []string,
	branchEdits map[string][]byte,
	branchDeletes []string,
) (string, string) {
	t.Helper()
	ctx := t.Context()
	var rootID string
	testutil.FailErr(t, "lookup project root id", f.sqlDB.QueryRowContext(
		ctx, `SELECT id FROM project_roots WHERE project_id = ? AND path = ? LIMIT 1`,
		testdbseed.DefaultProjectID, f.primary,
	).Scan(&rootID))

	_, err := f.svcs.Queue.Enqueue(ctx, api.WorkerTask{
		ID:              jobID,
		ParentSessionID: f.parent,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   f.primary,
		WorkspaceRootID: rootID,
		AgentType:       "implementer",
		Prompt:          "tb04 work " + jobID,
		Brief:           "tb04 brief " + jobID,
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: scopePaths},
	})
	testutil.FailErr(t, "enqueue "+jobID, err)

	claimed, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ClaimedBy:       "tb04-poller",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim "+jobID, err)
	if claimed.ID != jobID {
		t.Fatalf("claimed %q, want %q", claimed.ID, jobID)
	}

	branch := filepath.Join(f.dataDir, "worker-branches", jobID)
	tb04CopyDirFiles(t, f.primary, branch)
	baselinePath := testbaseline.Durable(t, rootID, f.sqlDB, jobID, f.primary)
	testbaseline.RecordBranch(t, rootID, f.primary, branch)

	bound, err := f.svcs.Queue.store.SetWorkerWorkspace(ctx, jobID, branch, baselinePath)
	testutil.FailErr(t, "bind workspace "+jobID, err)
	if !bound {
		t.Fatalf("SetWorkerWorkspace(%s) returned false", jobID)
	}

	tb04WriteFiles(t, branch, branchEdits)
	for _, rel := range branchDeletes {
		testutil.FailErr(t, "delete "+rel, os.Remove(filepath.Join(branch, filepath.FromSlash(rel))))
	}

	claimed.WorkspaceRoot = branch
	claimed.WorkspaceBaselinePath = baselinePath
	won, err := f.svcs.Queue.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "done " + jobID})
	testutil.FailErr(t, "complete "+jobID, err)
	if !won {
		t.Fatalf("Complete(%s) did not commit", jobID)
	}
	task, ok := f.svcs.Queue.Get(jobID)
	if !ok || task.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("job %s after complete = %+v, want pending", jobID, task)
	}
	return branch, baselinePath
}

func tb04RequireRejectCode(t *testing.T, err error, wantCode string) *toolrejection.ToolReject {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("expected ToolReject(%s), got %T: %v", wantCode, err, err)
	}
	if reject.Code != wantCode {
		t.Fatalf("reject code = %q, want %q (err=%v)", reject.Code, wantCode, err)
	}
	return reject
}

func tb04CountWorkerResults(t *testing.T, sqlDB *db.Store, jobID string) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count worker_results", sqlDB.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM worker_results WHERE worker_job_id = ?`, jobID,
	).Scan(&n))
	return n
}

func tb04SingleResultID(t *testing.T, sqlDB *db.Store, jobID string) string {
	t.Helper()
	var id string
	testutil.FailErr(t, "select worker_results.id", sqlDB.QueryRowContext(
		t.Context(), `SELECT id FROM worker_results WHERE worker_job_id = ?`, jobID,
	).Scan(&id))
	return id
}

// =============================================================================
// W04 · Promotion conflicts and atomicity (4 in-process slots)
// =============================================================================

// Slot W04.1: Atomic promotion commit — complete changeset lands or none does;
// durable commit failure rolls back every file effect and releases the apply lease.
func TestWEATB04_W04_AtomicPromotionCommitAndLeaseBoundary(t *testing.T) {
	ctx := t.Context()
	f := tb04NewSQLFixture(t, map[string][]byte{
		"edit.go":   []byte("package p\nvar v = 1\n"),
		"delete.go": []byte("package p\nvar gone = true\n"),
		"keep.go":   []byte("package p\nvar keep = 1\n"),
	})
	f.seedPendingWorker(
		t, "tb04-w04-atomic",
		[]string{"edit.go", "create.go", "delete.go"},
		map[string][]byte{
			"edit.go":   []byte("package p\nvar v = 2\n"),
			"create.go": []byte("package p\nvar created = 1\n"),
		},
		[]string{"delete.go"},
	)

	beforeHash := tb04HashTree(t, f.primary)

	// Inject durable commit fault inside SQLite so CommitPromotion aborts AFTER filesystem effects apply.
	_, err := f.sqlDB.ExecContext(ctx, `
		CREATE TRIGGER tb04_abort_merge_commit
		BEFORE UPDATE OF merge_status ON worker_jobs
		WHEN NEW.merge_status = 'merged'
		BEGIN
			SELECT RAISE(ABORT, 'tb04 injected durable commit failure');
		END`)
	testutil.FailErr(t, "create abort trigger", err)

	out, err := f.merge.PromoteOverlay(ctx, f.parent, "tb04-w04-atomic", api.PromoteOverlayInput{})
	if err == nil || !strings.Contains(err.Error(), "tb04 injected durable commit failure") {
		t.Fatalf("PromoteOverlay with commit fault = (%+v, %v), want injected error", out, err)
	}
	if out.Status == api.WorkerMergeStatusMerged {
		t.Fatalf("aborted promotion reported merged: %+v", out)
	}

	// Primary tree must be byte-for-byte identical to pre-promotion state (zero partial landing).
	if gotHash := tb04HashTree(t, f.primary); gotHash != beforeHash {
		t.Fatalf("primary tree hash after rolled-back commit = %s, want %s", gotHash, beforeHash)
	}
	// Lease must be released and job returned to pending.
	task, ok := f.svcs.Queue.Get("tb04-w04-atomic")
	if !ok || task.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("task after rolled-back commit = %+v, want pending", task)
	}
	var claimToken sql.NullString
	testutil.FailErr(t, "read merge_claim_token", f.sqlDB.QueryRowContext(
		ctx, `SELECT merge_claim_token FROM worker_jobs WHERE id = ?`, "tb04-w04-atomic",
	).Scan(&claimToken))
	if claimToken.Valid && claimToken.String != "" {
		t.Fatalf("merge_claim_token still held after rollback: %q", claimToken.String)
	}

	// Remove fault and promote cleanly: entire changeset lands atomically.
	_, err = f.sqlDB.ExecContext(ctx, `DROP TRIGGER tb04_abort_merge_commit`)
	testutil.FailErr(t, "drop abort trigger", err)

	out, err = f.merge.PromoteOverlay(ctx, f.parent, "tb04-w04-atomic", api.PromoteOverlayInput{})
	testutil.FailErr(t, "clean PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("clean promotion status = %q, want merged", out.Status)
	}

	expectedTree := t.TempDir()
	tb04WriteFiles(t, expectedTree, map[string][]byte{
		"edit.go":   []byte("package p\nvar v = 2\n"),
		"create.go": []byte("package p\nvar created = 1\n"),
		"keep.go":   []byte("package p\nvar keep = 1\n"),
	})
	if got, want := tb04HashTree(t, f.primary), tb04HashTree(t, expectedTree); got != want {
		t.Fatalf("primary tree hash after clean commit = %s, want %s", got, want)
	}

	// Second promotion on already-merged job must be rejected with OVERLAY_PROMOTE_NOT_PENDING.
	_, err = f.merge.PromoteOverlay(ctx, f.parent, "tb04-w04-atomic", api.PromoteOverlayInput{})
	tb04RequireRejectCode(t, err, OverlayPromoteNotPendingCode)
}

// Slot W04.2: Concurrent promotion conflict — two overlapping workers race behind a barrier;
// winner lands cleanly, loser fails with OVERLAY_PROMOTE_CONFLICT (worker bytes never silently win,
// clean files of loser do not partially land), invalid resolution returns OVERLAY_PROMOTE_RESOLUTIONS_REQUIRED,
// and explicit resolution lands the second worker cleanly. Also verified in both orders.
func TestWEATB04_W04_ConcurrentPromotionConflictNeverSilentlyWins(t *testing.T) {
	ctx := t.Context()

	t.Run("concurrent_barrier_race", func(t *testing.T) {
		f := tb04NewSQLFixture(t, map[string][]byte{
			"shared.go": []byte("package p\nfunc Value() int { return 0 }\n"),
		})
		f.seedPendingWorker(
			t, "tb04-race-a",
			[]string{"shared.go", "only_a.go"},
			map[string][]byte{
				"shared.go": []byte("package p\nfunc Value() int { return 1 }\n"),
				"only_a.go": []byte("package p\nvar a = 1\n"),
			},
			nil,
		)
		f.seedPendingWorker(
			t, "tb04-race-b",
			[]string{"shared.go", "only_b.go"},
			map[string][]byte{
				"shared.go": []byte("package p\nfunc Value() int { return 2 }\n"),
				"only_b.go": []byte("package p\nvar b = 2\n"),
			},
			nil,
		)

		start := make(chan struct{})
		type outcome struct {
			jobID string
			res   api.WorkerMergeResult
			err   error
		}
		results := make(chan outcome, 2)
		for _, id := range []string{"tb04-race-a", "tb04-race-b"} {
			go func(jobID string) {
				<-start
				res, err := f.merge.PromoteOverlay(ctx, f.parent, jobID, api.PromoteOverlayInput{})
				results <- outcome{jobID: jobID, res: res, err: err}
			}(id)
		}
		close(start)

		first := <-results
		second := <-results
		var winner, loser outcome
		switch {
		case first.err == nil && second.err != nil:
			winner, loser = first, second
		case second.err == nil && first.err != nil:
			winner, loser = second, first
		default:
			t.Fatalf("expected exactly one winner and one conflict, got first=(%s,%v) second=(%s,%v)",
				first.jobID, first.err, second.jobID, second.err)
		}
		if winner.res.Status != api.WorkerMergeStatusMerged {
			t.Fatalf("winner %s status = %q, want merged", winner.jobID, winner.res.Status)
		}
		tb04RequireRejectCode(t, loser.err, OverlayPromoteConflictCode)

		// Winner's bytes must be intact; loser's clean file must NOT have landed partially.
		wantShared := "package p\nfunc Value() int { return 1 }\n"
		loserOnlyFile := "only_b.go"
		if winner.jobID == "tb04-race-b" {
			wantShared = "package p\nfunc Value() int { return 2 }\n"
			loserOnlyFile = "only_a.go"
		}
		rawShared, err := os.ReadFile(filepath.Join(f.primary, "shared.go"))
		testutil.FailErr(t, "read shared.go", err)
		if string(rawShared) != wantShared {
			t.Fatalf("shared.go = %q, want winner bytes %q", rawShared, wantShared)
		}
		if _, err := os.Stat(filepath.Join(f.primary, loserOnlyFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("loser's clean file %s leaked onto primary tree: %v", loserOnlyFile, err)
		}
		beforeInvalidHash := tb04HashTree(t, f.primary)

		// Invalid hunk resolution on loser must fail with OVERLAY_PROMOTE_RESOLUTIONS_REQUIRED
		// and leave primary untouched.
		_, err = f.merge.PromoteOverlay(ctx, f.parent, loser.jobID, api.PromoteOverlayInput{
			Resolutions: []api.WorkerPromoteResolution{{
				Path: "shared.go",
				Hunks: []api.WorkerPromoteHunkResolution{{
					StartLine: 999,
					EndLine:   999,
					Content:   "invalid\n",
				}},
			}},
		})
		tb04RequireRejectCode(t, err, OverlayPromoteResolutionsRequiredCode)
		if gotHash := tb04HashTree(t, f.primary); gotHash != beforeInvalidHash {
			t.Fatalf("invalid resolution modified primary tree: %s != %s", gotHash, beforeInvalidHash)
		}

		// Explicit valid resolution lands loser's complete changeset.
		resolvedOut, err := f.merge.PromoteOverlay(ctx, f.parent, loser.jobID, api.PromoteOverlayInput{
			Resolutions: []api.WorkerPromoteResolution{{
				Path:    "shared.go",
				Content: "package p\nfunc Value() int { return 3 }\n",
			}},
		})
		testutil.FailErr(t, "resolve loser", err)
		if resolvedOut.Status != api.WorkerMergeStatusMerged {
			t.Fatalf("resolved loser status = %q, want merged", resolvedOut.Status)
		}
		rawShared, err = os.ReadFile(filepath.Join(f.primary, "shared.go"))
		testutil.FailErr(t, "read resolved shared.go", err)
		if string(rawShared) != "package p\nfunc Value() int { return 3 }\n" {
			t.Fatalf("resolved shared.go = %q", rawShared)
		}
		if _, err := os.Stat(filepath.Join(f.primary, loserOnlyFile)); err != nil {
			t.Fatalf("loser's clean file %s missing after resolution: %v", loserOnlyFile, err)
		}
	})

	// Verify both explicit promotion orders (A->B and B->A) enforce conflict without silent overwrite.
	for _, order := range [][]string{{"a", "b"}, {"b", "a"}} {
		t.Run("order_"+order[0]+"_then_"+order[1], func(t *testing.T) {
			f := tb04NewSQLFixture(t, map[string][]byte{
				"shared.go": []byte("package p\nconst Who = \"base\"\n"),
			})
			f.seedPendingWorker(t, "tb04-ord-a", []string{"shared.go"}, map[string][]byte{
				"shared.go": []byte("package p\nconst Who = \"a\"\n"),
			}, nil)
			f.seedPendingWorker(t, "tb04-ord-b", []string{"shared.go"}, map[string][]byte{
				"shared.go": []byte("package p\nconst Who = \"b\"\n"),
			}, nil)

			firstID := "tb04-ord-" + order[0]
			secondID := "tb04-ord-" + order[1]
			out, err := f.merge.PromoteOverlay(ctx, f.parent, firstID, api.PromoteOverlayInput{})
			testutil.FailErr(t, "promote first "+firstID, err)
			if out.Status != api.WorkerMergeStatusMerged {
				t.Fatalf("first status = %q", out.Status)
			}
			afterFirstHash := tb04HashTree(t, f.primary)

			_, err = f.merge.PromoteOverlay(ctx, f.parent, secondID, api.PromoteOverlayInput{})
			tb04RequireRejectCode(t, err, OverlayPromoteConflictCode)
			if got := tb04HashTree(t, f.primary); got != afterFirstHash {
				t.Fatalf("conflicting second worker altered primary: %s != %s", got, afterFirstHash)
			}
		})
	}
}

// Slot W04.3: Overlapping opaque/binary file conflict is never silently dropped or
// silently overwritten; explicit keep_theirs / keep_ours preserves exact binary bytes.
func TestWEATB04_W04_OverlappingOpaqueBinaryConflictNeverSilentlyDropped(t *testing.T) {
	ctx := t.Context()
	baseBin := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x00, 0x01, 0x02, 0xff}
	primaryBin := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xaa, 0xbb, 0xcc, 0xff}
	workerBin := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x11, 0x22, 0x33, 0xff}

	for _, tc := range []struct {
		name      string
		action    api.WorkerPromoteResolutionAction
		wantBytes []byte
	}{
		{name: "keep_theirs", action: api.WorkerPromoteResolutionActionKeepTheirs, wantBytes: workerBin},
		{name: "keep_ours", action: api.WorkerPromoteResolutionActionKeepOurs, wantBytes: primaryBin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tb04NewSQLFixture(t, map[string][]byte{
				"asset.bin": baseBin,
				"notes.txt": []byte("base note\n"),
			})
			f.seedPendingWorker(
				t, "tb04-bin-"+tc.name,
				[]string{"asset.bin", "notes.txt"},
				map[string][]byte{
					"asset.bin": workerBin,
					"notes.txt": []byte("worker note\n"),
				},
				nil,
			)
			// Primary tree diverges on the opaque binary file during review.
			tb04WriteFiles(t, f.primary, map[string][]byte{"asset.bin": primaryBin})
			beforeHash := tb04HashTree(t, f.primary)

			// Automatic promote must reject with OVERLAY_PROMOTE_CONFLICT and report artifact conflict.
			conflictOut, err := f.merge.PromoteOverlay(ctx, f.parent, "tb04-bin-"+tc.name, api.PromoteOverlayInput{})
			tb04RequireRejectCode(t, err, OverlayPromoteConflictCode)
			if len(conflictOut.Conflicts) != 1 || conflictOut.Conflicts[0].Path != "asset.bin" ||
				conflictOut.Conflicts[0].Reason != api.WorkerPromoteReasonArtifact {
				t.Fatalf("binary conflict = %+v, want single artifact conflict on asset.bin", conflictOut.Conflicts)
			}
			// Neither primary binary nor notes.txt may be altered or dropped.
			if got := tb04HashTree(t, f.primary); got != beforeHash {
				t.Fatalf("opaque conflict modified primary tree: %s != %s", got, beforeHash)
			}

			// Explicit resolution lands the chosen binary bytes and the clean text edit atomically.
			out, err := f.merge.PromoteOverlay(ctx, f.parent, "tb04-bin-"+tc.name, api.PromoteOverlayInput{
				Resolutions: []api.WorkerPromoteResolution{{
					Path:   "asset.bin",
					Action: tc.action,
				}},
			})
			testutil.FailErr(t, "promote binary with "+tc.name, err)
			if out.Status != api.WorkerMergeStatusMerged {
				t.Fatalf("status = %q, want merged", out.Status)
			}
			gotBin, err := os.ReadFile(filepath.Join(f.primary, "asset.bin"))
			testutil.FailErr(t, "read asset.bin", err)
			if string(gotBin) != string(tc.wantBytes) {
				t.Fatalf("asset.bin bytes = %x, want %x", gotBin, tc.wantBytes)
			}
			gotNotes, err := os.ReadFile(filepath.Join(f.primary, "notes.txt"))
			testutil.FailErr(t, "read notes.txt", err)
			if string(gotNotes) != "worker note\n" {
				t.Fatalf("notes.txt = %q, want worker note", gotNotes)
			}
		})
	}
}

// Slot W04.4: Promotion lease expiry and reclaim prevents duplicate landing and fences stale claims.
func TestWEATB04_W04_PromotionLeaseExpiryReclaimPreventsDuplicateLanding(t *testing.T) {
	ctx := t.Context()
	f := tb04NewSQLFixture(t, map[string][]byte{
		"app.go": []byte("package app\nconst Ver = 1\n"),
	})
	f.seedPendingWorker(
		t, "tb04-lease-job",
		[]string{"app.go"},
		map[string][]byte{"app.go": []byte("package app\nconst Ver = 2\n")},
		nil,
	)

	// Acquire a real apply lease on the pending job to simulate an in-flight writer.
	staleToken, ok, err := f.svcs.Promotions.BeginMergeApply(ctx, "tb04-lease-job")
	testutil.FailErr(t, "BeginMergeApply", err)
	if !ok || staleToken == "" {
		t.Fatalf("BeginMergeApply = (%q, %v), want claim", staleToken, ok)
	}

	// While the lease is live (unexpired), another PromoteOverlay must be refused.
	_, err = f.merge.PromoteOverlay(ctx, f.parent, "tb04-lease-job", api.PromoteOverlayInput{})
	tb04RequireRejectCode(t, err, OverlayPromoteNotPendingCode)

	// Simulate an interrupted promotion that wrote partial primary bytes via a real promoteTransaction
	// and then stalled until its lease expired.
	task, ok := f.svcs.Queue.Get("tb04-lease-job")
	if !ok {
		t.Fatal("missing tb04-lease-job")
	}
	promote := PromoteRootsForTask(task, f.merge.taskRootRefs(ctx, task))
	tx, err := beginPromoteTransaction(f.merge.hostDataDirFor(task), task.ID, []promoteMutation{{
		path:           "app.go",
		original:       []byte("package app\nconst Ver = 1\n"),
		originalExists: true,
		originalMode:   0o644,
		target:         []byte("package app\nconst Ver = 999\n"),
		targetExists:   true,
	}})
	testutil.FailErr(t, "beginPromoteTransaction", err)
	testutil.FailErr(t, "partial apply", tx.apply(ctx, promote, task))

	// Expire the stalled claim's lease in SQLite.
	_, err = f.sqlDB.ExecContext(ctx, `
		UPDATE worker_jobs SET merge_lease_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, "tb04-lease-job")
	testutil.FailErr(t, "expire lease", err)

	// Reclaim via PromoteOverlay rolls back the interrupted transaction, restores original, and lands once.
	out, err := f.merge.PromoteOverlay(ctx, f.parent, "tb04-lease-job", api.PromoteOverlayInput{})
	testutil.FailErr(t, "reclaim and promote", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("reclaimed promote status = %q, want merged", out.Status)
	}
	gotApp, err := os.ReadFile(filepath.Join(f.primary, "app.go"))
	testutil.FailErr(t, "read app.go", err)
	if string(gotApp) != "package app\nconst Ver = 2\n" {
		t.Fatalf("app.go after reclaim = %q, want Ver=2", gotApp)
	}

	// Stale writer attempting to release its old claim must be fenced with errMergeApplyClaimLost.
	if err := f.svcs.Promotions.ReleaseMergeApply(ctx, "tb04-lease-job", staleToken); !errors.Is(err, errMergeApplyClaimLost) {
		t.Fatalf("stale ReleaseMergeApply = %v, want errMergeApplyClaimLost", err)
	}
	// Duplicate PromoteOverlay must be rejected with OVERLAY_PROMOTE_NOT_PENDING.
	_, err = f.merge.PromoteOverlay(ctx, f.parent, "tb04-lease-job", api.PromoteOverlayInput{})
	tb04RequireRejectCode(t, err, OverlayPromoteNotPendingCode)
}

// =============================================================================
// W10 · Restart and retention boundaries (5 in-process slots)
// =============================================================================

type tb04BarrierExecutor struct {
	mu       sync.Mutex
	calls    int
	failOnce bool
	onExec   func(ctx context.Context, call int, task api.WorkerTask) (api.WorkerResult, error)
}

func (e *tb04BarrierExecutor) Execute(ctx context.Context, task api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	fn := e.onExec
	failOnce := e.failOnce
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, call, task)
	}
	if failOnce && call == 1 {
		return api.WorkerResult{}, errors.New("tb04 transient crash before report validation")
	}
	return api.WorkerResult{Status: "complete", Summary: "validated report"}, nil
}

func (*tb04BarrierExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }

type tb04OutcomeTracker struct {
	mu          sync.Mutex
	queue       *SQLQueue
	projectErr  error
	projections int
	proofs      int
	delivered   int
	onProject   func()
}

func (o *tb04OutcomeTracker) TaskByID(id string) (workeroutcomes.SummaryInput, bool) {
	task, ok := o.queue.Get(id)
	if !ok || task == nil {
		return workeroutcomes.SummaryInput{}, false
	}
	return workeroutcomes.SummaryInput{
		ParentSessionID: task.ParentSessionID,
		ProjectID:       task.ProjectID,
	}, true
}

func (o *tb04OutcomeTracker) ProjectResult(_ context.Context, _ workeroutcomes.SummaryInput, res api.WorkerResult) (string, error) {
	o.mu.Lock()
	o.projections++
	err := o.projectErr
	fn := o.onProject
	o.mu.Unlock()
	if fn != nil {
		fn()
	}
	if err != nil {
		return "", err
	}
	return res.Status, nil
}

func (o *tb04OutcomeTracker) ProjectFailure(context.Context, workeroutcomes.SummaryInput, error) error {
	return nil
}

func (o *tb04OutcomeTracker) RecordTerminalProof(context.Context, string, string, string) error {
	o.mu.Lock()
	o.proofs++
	o.mu.Unlock()
	return nil
}

func (o *tb04OutcomeTracker) EnvelopeForTerminal(context.Context, string, string) anchor.Envelope {
	return anchor.Envelope{}
}

func (o *tb04OutcomeTracker) Terminal(context.Context, string, string) {
	o.mu.Lock()
	o.delivered++
	o.mu.Unlock()
}

func (o *tb04OutcomeTracker) AfterTerminal(context.Context, string, string, anchor.Envelope) {}
func (o *tb04OutcomeTracker) NudgeLegFinished(context.Context, string, time.Time, string)    {}
func (o *tb04OutcomeTracker) ShouldNudge(context.Context, string, string, string) bool       { return false }
func (o *tb04OutcomeTracker) Arm(context.Context, string, string)                            {}

type tb04FailAckQueue struct {
	*SQLQueue
	failAck atomic.Bool
}

func (q *tb04FailAckQueue) MarkOutcomeDelivered(ctx context.Context, id string) error {
	if q.failAck.Load() {
		return errors.New("tb04 crash before outcome acknowledgement")
	}
	return q.SQLQueue.MarkOutcomeDelivered(ctx, id)
}

type tb04PoisonReportHost struct{}

func (tb04PoisonReportHost) PromptHostTurn(context.Context, string, sessionstore.PromptSubmissionOrigin, string) (*promptresult.Result, error) {
	return nil, errors.New("unexpected closeout host turn for accepted report")
}

func (tb04PoisonReportHost) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, errors.New("accepted report must not be re-evaluated after restart")
}

func tb04StockHints(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return cfg
}

var (
	tb04PipelineOnce sync.Once
	tb04Pipeline     *oar.GuardPipeline
	tb04PipelineErr  error
)

func tb04OARPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	tb04PipelineOnce.Do(func() {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			tb04PipelineErr = errors.New("runtime.Caller failed")
			return
		}
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		profile := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
		if err := anchorcatalog.InstallBundled(); err != nil {
			tb04PipelineErr = err
			return
		}
		declaration, err := oar.LoadEngineDeclaration(profile)
		if err != nil {
			tb04PipelineErr = err
			return
		}
		oar.InstallEngineDeclaration(declaration)
		schemaDir := filepath.Join(filepath.Dir(root), "schemas")
		loader, err := oar.NewLoader(schemaDir)
		if err != nil {
			tb04PipelineErr = err
			return
		}
		rs, err := loader.LoadEffectivePolicy()
		if err != nil {
			tb04PipelineErr = err
			return
		}
		p := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
		p.EnableAnchor(oar.AnchorWorkerReportCheck)
		tb04Pipeline = p
	})
	if tb04Pipeline == nil {
		testutil.FailErr(t, "tb04 OAR pipeline", tb04PipelineErr)
	}
	return tb04Pipeline
}

// Slot W10.1: Restart boundary around report validation — enters the real owning report-validation
// path (workercompletion.AcceptWorkerReport + SQLite child transcript persistence +
// workercloseout.FinalizeWorkerSummaryForChild) with a deterministic test-side barrier on both sides
// of validation persistence, cleanly separated from the Queue.Complete result-handle commit phase.
func TestWEATB04_W10_RestartBoundaryAroundReportValidation(t *testing.T) {
	ctx := t.Context()
	hints := tb04StockHints(t)
	pipeline := tb04OARPipeline(t)

	// Helper that creates a real child session bound to the worker job in SQLite.
	bindChildSession := func(t *testing.T, f *tb04SQLFixture, jobID string) string {
		t.Helper()
		repos := sessionstore.NewSQL(f.sqlDB)
		parent, err := repos.Sessions.Get(ctx, f.parent)
		testutil.FailErr(t, "get parent session", err)
		child, err := repos.Sessions.CreateChild(ctx, parent, api.SpawnChildRequest{
			AgentType:   "security-reviewer",
			Prompt:      "validate report",
			WorkerJobID: jobID,
		})
		testutil.FailErr(t, "CreateChild", err)
		testutil.FailErr(t, "SetChildSessionID", f.svcs.Queue.store.SetChildSessionID(ctx, jobID, child.ID))
		return child.ID
	}

	// Verify the real report-validation owner rejects invalid reports before accepting valid ones.
	validateReport := func(t *testing.T, jobID, childID, brief string) *api.WorkerReportAcceptance {
		t.Helper()
		badInput := workercompletion.WorkerSummaryEvalInput{
			AgentType:      "security-reviewer",
			ChildSessionID: childID,
			Hints:          hints,
			Pipeline:       pipeline,
			Report:         workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: ""},
		}
		if badReceipt, badErr := workercompletion.AcceptWorkerReport(ctx, jobID, badInput); badReceipt != nil || badErr == nil {
			t.Fatalf("AcceptWorkerReport accepted empty brief: receipt=%+v err=%v", badReceipt, badErr)
		} else if reject := toolrejection.AsToolReject(badErr); reject == nil || reject.Code != "WORKER_TURN_NO_PROSE" {
			t.Fatalf("empty brief rejection = %v (reject=%+v), want WORKER_TURN_NO_PROSE", badErr, reject)
		}

		validInput := workercompletion.WorkerSummaryEvalInput{
			AgentType:      "security-reviewer",
			ChildSessionID: childID,
			Hints:          hints,
			Pipeline:       pipeline,
			Report:         workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: brief},
		}
		receipt, err := workercompletion.AcceptWorkerReport(ctx, jobID, validInput)
		testutil.FailErr(t, "AcceptWorkerReport valid", err)
		if receipt == nil || receipt.WorkerID != jobID || receipt.Report == nil || receipt.Report.Brief != brief {
			t.Fatalf("unexpected acceptance receipt: %+v", receipt)
		}
		return receipt
	}

	persistAcceptedReport := func(t *testing.T, sqlDB *db.Store, childID, jobID, msgID string, receipt *api.WorkerReportAcceptance) {
		t.Helper()
		repos := sessionstore.NewSQL(sqlDB)
		testutil.FailErr(t, "AppendMessages complete_leg", repos.AppendMessages(ctx, childID, api.Message{
			ID:       msgID,
			Role:     api.MessageRoleTool,
			WorkerID: jobID,
			ToolResult: &api.ToolResult{
				Tool:         "complete_leg",
				Outcome:      api.ToolResultOutcomeCompleted,
				WorkerReport: receipt,
			},
		}))
	}

	t.Run("restart_before_report_validation_persists", func(t *testing.T) {
		f := tb04NewSQLFixture(t, nil)
		id, err := f.svcs.Queue.Enqueue(ctx, api.WorkerTask{
			ID: "tb04-w10-rv-before", ParentSessionID: f.parent, ProjectID: testdbseed.DefaultProjectID,
			WorkspacePath: f.primary, AgentType: "security-reviewer", Prompt: "validate report", Brief: "brief",
		})
		testutil.FailErr(t, "enqueue", err)
		childID := bindChildSession(t, f, id)

		// Attempt 1: claim, enter real AcceptWorkerReport, hit test-side barrier BEFORE persisting
		// the accepted report into SQLite (and before Queue.Complete), then simulate abrupt crash + restart.
		claimed1, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
			ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-1", ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "claim attempt 1", err)

		// Run real report validation behind a deterministic barrier; simulate crash before persisting.
		type validationResult struct {
			receipt *api.WorkerReportAcceptance
			err     error
		}
		validatedCh := make(chan validationResult, 1)
		releaseCrash := make(chan struct{})
		go func() {
			r, vErr := workercompletion.AcceptWorkerReport(ctx, id, workercompletion.WorkerSummaryEvalInput{
				AgentType:      "security-reviewer",
				ChildSessionID: childID,
				Hints:          hints,
				Pipeline:       pipeline,
				Report:         workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: "validated before crash barrier"},
			})
			validatedCh <- validationResult{receipt: r, err: vErr}
			<-releaseCrash
			// Simulated process death before AppendMessages / FinalizeWorkerSummaryForChild / Queue.Complete.
		}()
		vr := <-validatedCh
		close(releaseCrash)
		testutil.FailErr(t, "AcceptWorkerReport attempt 1", vr.err)
		if vr.receipt == nil || vr.receipt.WorkerID != id {
			t.Fatalf("attempt 1 receipt = %+v, want accepted receipt for %s", vr.receipt, id)
		}

		if tb04CountWorkerResults(t, f.sqlDB, id) != 0 {
			t.Fatal("unexpected worker_results row before validation persisted")
		}
		_, err = f.sqlDB.ExecContext(ctx, `UPDATE worker_jobs SET lease_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, id)
		testutil.FailErr(t, "expire claim lease", err)
		f.reopen(t)

		// After restart, no accepted report exists in the reopened child transcript.
		repos := sessionstore.NewSQL(f.sqlDB)
		msgs, err := repos.Transcript.GetWorkerJobMessages(ctx, childID, id)
		testutil.FailErr(t, "GetWorkerJobMessages after pre-persist crash", err)
		if _, _, ok := workercompletion.AcceptedWorkerReport(msgs, id); ok {
			t.Fatal("pre-persist crash unexpectedly persisted an accepted report")
		}

		// Recover expired claim, re-claim, validate+persist report, finalize via workercloseout, and commit once.
		_, err = f.svcs.Queue.RecoverExpiredClaims(ctx)
		testutil.FailErr(t, "RecoverExpiredClaims", err)
		claimed2, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
			ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-2", ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "claim attempt 2", err)
		if claimed2.ID != id || claimed2.ClaimToken == claimed1.ClaimToken {
			t.Fatalf("reclaimed = %+v, want new claim token for %s", claimed2, id)
		}

		receipt := validateReport(t, id, childID, "validated on attempt 2")
		persistAcceptedReport(t, f.sqlDB, childID, id, "tb04-msg-rv-before-2", receipt)

		outcome, err := workercloseout.FinalizeWorkerSummaryForChild(
			ctx, repos.Transcript, tb04PoisonReportHost{}, childID, "security-reviewer",
			workercloseout.WorkerSummaryFinalizeOpts{WorkerJobID: id, Ledger: tb04PoisonReportHost{}},
		)
		testutil.FailErr(t, "FinalizeWorkerSummaryForChild", err)
		if outcome.Provenance != "complete_leg" || outcome.Summary != "validated on attempt 2" {
			t.Fatalf("finalized outcome = %+v, want complete_leg with attempt 2 brief", outcome)
		}

		won, err := f.svcs.Queue.Complete(ctx, claimed2, api.WorkerResult{
			Status:           outcome.Status,
			Summary:          outcome.Summary,
			CompletionReport: workercompletion.ReportWire(outcome.Report),
			Grounding:        outcome.Grounding,
		})
		testutil.FailErr(t, "complete attempt 2", err)
		if !won {
			t.Fatal("attempt 2 completion did not win")
		}
		if n := tb04CountWorkerResults(t, f.sqlDB, id); n != 1 {
			t.Fatalf("worker_results count = %d, want 1", n)
		}
	})

	t.Run("restart_after_report_validation_persists_before_result_commit", func(t *testing.T) {
		f := tb04NewSQLFixture(t, nil)
		id, err := f.svcs.Queue.Enqueue(ctx, api.WorkerTask{
			ID: "tb04-w10-rv-after", ParentSessionID: f.parent, ProjectID: testdbseed.DefaultProjectID,
			WorkspacePath: f.primary, AgentType: "security-reviewer", Prompt: "validate report", Brief: "brief",
		})
		testutil.FailErr(t, "enqueue", err)
		childID := bindChildSession(t, f, id)

		claimed1, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
			ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-1", ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "claim attempt 1", err)

		// Validate report via real AcceptWorkerReport and persist the accepted report to SQLite,
		// then hit the restart barrier BEFORE Queue.Complete commits any worker_results row.
		receipt := validateReport(t, id, childID, "validated report persisted before crash")
		persistAcceptedReport(t, f.sqlDB, childID, id, "tb04-msg-rv-after-1", receipt)

		if n := tb04CountWorkerResults(t, f.sqlDB, id); n != 0 {
			t.Fatalf("worker_results count before Queue.Complete = %d, want 0", n)
		}
		_, err = f.sqlDB.ExecContext(ctx, `UPDATE worker_jobs SET lease_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, id)
		testutil.FailErr(t, "expire claim lease", err)
		f.reopen(t)

		// After restart, recover expired claim and finalize using the reopened SQLite transcript.
		// tb04PoisonReportHost guarantees FinalizeWorkerSummaryForChild reuses the persisted
		// WorkerReportAcceptance without re-evaluating or running a host closeout turn.
		_, err = f.svcs.Queue.RecoverExpiredClaims(ctx)
		testutil.FailErr(t, "RecoverExpiredClaims", err)
		claimed2, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
			ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-2", ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "claim attempt 2", err)
		if claimed2.ID != id || claimed2.ClaimToken == claimed1.ClaimToken {
			t.Fatalf("reclaimed = %+v, want new claim token for %s", claimed2, id)
		}

		repos := sessionstore.NewSQL(f.sqlDB)
		outcome, err := workercloseout.FinalizeWorkerSummaryForChild(
			ctx, repos.Transcript, tb04PoisonReportHost{}, childID, "security-reviewer",
			workercloseout.WorkerSummaryFinalizeOpts{WorkerJobID: id, Ledger: tb04PoisonReportHost{}},
		)
		testutil.FailErr(t, "FinalizeWorkerSummaryForChild after restart", err)
		if outcome.Provenance != "complete_leg" || outcome.Summary != "validated report persisted before crash" {
			t.Fatalf("finalized outcome after restart = %+v, want persisted accepted report", outcome)
		}

		// Commit the finalized outcome once via Queue.Complete, then verify another restart does not duplicate it.
		won, err := f.svcs.Queue.Complete(ctx, claimed2, api.WorkerResult{
			Status:           outcome.Status,
			Summary:          outcome.Summary,
			CompletionReport: workercompletion.ReportWire(outcome.Report),
			Grounding:        outcome.Grounding,
		})
		testutil.FailErr(t, "Complete after validation recovery", err)
		if !won {
			t.Fatal("completion did not commit")
		}
		firstResultID := tb04SingleResultID(t, f.sqlDB, id)

		f.reopen(t)
		exec := &tb04BarrierExecutor{}
		tracker := &tb04OutcomeTracker{queue: f.svcs.Queue}
		bridge := &SessionOutcomeBridge{Workers: tracker, Loop: tracker, Results: tracker, State: tracker, Closure: tracker}
		poller := NewLocalWorkerPoller(f.svcs.Queue, exec, DefaultWorkersConfig(), bridge)
		testutil.FailErr(t, "maintain after restart", poller.maintain(ctx))

		if exec.calls != 0 {
			t.Fatalf("executor re-ran %d times after committed validated report", exec.calls)
		}
		if n := tb04CountWorkerResults(t, f.sqlDB, id); n != 1 {
			t.Fatalf("worker_results count = %d, want 1", n)
		}
		if got := tb04SingleResultID(t, f.sqlDB, id); got != firstResultID {
			t.Fatalf("result handle changed across restart: %q != %q", got, firstResultID)
		}
	})
}

// Slot W10.2: Restart boundary around result-handle commit — aborted commit leaves zero results
// and recovers cleanly; post-commit restart fences stale attempt so no duplicate result handle exists.
func TestWEATB04_W10_RestartBoundaryAroundResultHandleCommit(t *testing.T) {
	ctx := t.Context()
	f := tb04NewSQLFixture(t, nil)
	id, err := f.svcs.Queue.Enqueue(ctx, api.WorkerTask{
		ID: "tb04-w10-handle", ParentSessionID: f.parent, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: f.primary, AgentType: "implementer", Prompt: "commit handle", Brief: "brief",
	})
	testutil.FailErr(t, "enqueue", err)

	claimed, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-1", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim", err)

	// Side 1: inject failure right at worker_results insert (result-handle commit boundary).
	_, err = f.sqlDB.ExecContext(ctx, `
		CREATE TRIGGER tb04_abort_result_handle
		BEFORE INSERT ON worker_results
		BEGIN
			SELECT RAISE(ABORT, 'tb04 crash at result-handle insert');
		END`)
	testutil.FailErr(t, "create result-handle abort trigger", err)

	won, err := f.svcs.Queue.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "first attempt"})
	if err == nil || won {
		t.Fatalf("Complete with result-handle fault = (%v, %v), want error", won, err)
	}
	if n := tb04CountWorkerResults(t, f.sqlDB, id); n != 0 {
		t.Fatalf("rolled-back result-handle commit left %d rows", n)
	}
	task, ok := f.svcs.Queue.Get(id)
	if !ok || task.Status != api.WorkerStatusRunning {
		t.Fatalf("job status after rolled-back handle commit = %+v, want running", task)
	}

	// Remove fault, simulate restart by reopening DB, then commit the result handle cleanly.
	_, err = f.sqlDB.ExecContext(ctx, `DROP TRIGGER tb04_abort_result_handle`)
	testutil.FailErr(t, "drop trigger", err)
	f.reopen(t)

	won, err = f.svcs.Queue.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "committed after restart"})
	testutil.FailErr(t, "complete after restart", err)
	if !won {
		t.Fatal("Complete did not commit after restart")
	}
	committedHandle := tb04SingleResultID(t, f.sqlDB, id)

	// Side 2: restart immediately after result-handle commit; a stale/duplicate Complete call must not
	// create a second result handle or mutate the committed one.
	f.reopen(t)
	dupWon, dupErr := f.svcs.Queue.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "duplicate attempt"})
	testutil.FailErr(t, "duplicate Complete after restart", dupErr)
	if dupWon {
		t.Fatal("stale duplicate Complete must not win after result-handle commit")
	}
	if n := tb04CountWorkerResults(t, f.sqlDB, id); n != 1 {
		t.Fatalf("worker_results count after duplicate attempt = %d, want 1", n)
	}
	if got := tb04SingleResultID(t, f.sqlDB, id); got != committedHandle {
		t.Fatalf("result handle mutated: %q != %q", got, committedHandle)
	}
}

// Slot W10.3: Restart boundary around parent projection — crash before/during projection and
// crash between projection and outcome acknowledgement both keep the committed result handle intact
// and finish delivery cleanly on restart.
func TestWEATB04_W10_RestartBoundaryAroundParentProjection(t *testing.T) {
	ctx := t.Context()
	f := tb04NewSQLFixture(t, nil)
	id, err := f.svcs.Queue.Enqueue(ctx, api.WorkerTask{
		ID: "tb04-w10-proj", ParentSessionID: f.parent, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: f.primary, AgentType: "implementer", Prompt: "project outcome", Brief: "brief",
	})
	testutil.FailErr(t, "enqueue", err)

	claimed, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-1", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim", err)
	won, err := f.svcs.Queue.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "ready for projection"})
	testutil.FailErr(t, "complete", err)
	if !won {
		t.Fatal("complete did not commit")
	}
	handleID := tb04SingleResultID(t, f.sqlDB, id)

	// Side 1: crash during parent projection (ProjectResult fails before RecordTerminalProof / ack).
	tracker1 := &tb04OutcomeTracker{queue: f.svcs.Queue, projectErr: errors.New("tb04 crash during parent projection")}
	bridge1 := &SessionOutcomeBridge{Workers: tracker1, Loop: tracker1, Results: tracker1, State: tracker1, Closure: tracker1}
	poller1 := NewLocalWorkerPoller(f.svcs.Queue, &tb04BarrierExecutor{}, DefaultWorkersConfig(), bridge1)
	if err := poller1.deliverPendingOutcomes(ctx); err == nil {
		t.Fatal("expected projection failure")
	}
	if tracker1.delivered != 0 {
		t.Fatalf("delivered = %d after failed projection, want 0", tracker1.delivered)
	}

	// Restart (reopen DB): committed result handle is intact and outcome is still pending.
	f.reopen(t)
	if tb04CountWorkerResults(t, f.sqlDB, id) != 1 || tb04SingleResultID(t, f.sqlDB, id) != handleID {
		t.Fatal("result handle lost or altered across projection crash")
	}
	pending, err := f.svcs.Queue.ListPendingOutcomes(ctx)
	testutil.FailErr(t, "ListPendingOutcomes after crash 1", err)
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending outcomes = %+v, want [%s]", pending, id)
	}

	// Side 2: parent projection succeeds, then crash right before MarkOutcomeDelivered.
	failAckQ := &tb04FailAckQueue{SQLQueue: f.svcs.Queue}
	failAckQ.failAck.Store(true)
	tracker2 := &tb04OutcomeTracker{queue: f.svcs.Queue}
	bridge2 := &SessionOutcomeBridge{Workers: tracker2, Loop: tracker2, Results: tracker2, State: tracker2, Closure: tracker2}
	poller2 := NewLocalWorkerPoller(failAckQ, &tb04BarrierExecutor{}, DefaultWorkersConfig(), bridge2)
	if err := poller2.deliverPendingOutcomes(ctx); err == nil {
		t.Fatal("expected ack failure")
	}
	if tracker2.projections != 1 || tracker2.proofs != 1 || tracker2.delivered != 0 {
		t.Fatalf("side 2 counts: projections=%d proofs=%d delivered=%d", tracker2.projections, tracker2.proofs, tracker2.delivered)
	}

	// Restart again: pending outcome replays cleanly, marks delivered, and result handle stays intact.
	f.reopen(t)
	tracker3 := &tb04OutcomeTracker{queue: f.svcs.Queue}
	bridge3 := &SessionOutcomeBridge{Workers: tracker3, Loop: tracker3, Results: tracker3, State: tracker3, Closure: tracker3}
	poller3 := NewLocalWorkerPoller(f.svcs.Queue, &tb04BarrierExecutor{}, DefaultWorkersConfig(), bridge3)
	testutil.FailErr(t, "deliverPendingOutcomes after restart 2", poller3.deliverPendingOutcomes(ctx))
	if tracker3.projections != 1 || tracker3.proofs != 1 || tracker3.delivered != 1 {
		t.Fatalf("post-restart counts: projections=%d proofs=%d delivered=%d", tracker3.projections, tracker3.proofs, tracker3.delivered)
	}
	pending, err = f.svcs.Queue.ListPendingOutcomes(ctx)
	testutil.FailErr(t, "ListPendingOutcomes after ack", err)
	if len(pending) != 0 {
		t.Fatalf("pending outcomes remaining after delivery: %+v", pending)
	}
	if tb04CountWorkerResults(t, f.sqlDB, id) != 1 || tb04SingleResultID(t, f.sqlDB, id) != handleID {
		t.Fatal("result handle altered after parent projection recovery")
	}
}

// Slot W10.4: Restart boundary around promotion commit — crash before CommitPromotion rolls back
// and resumes cleanly on restart; crash after CommitPromotion settles journal without rollback or duplicate landing.
func TestWEATB04_W10_RestartBoundaryAroundPromotionCommit(t *testing.T) {
	ctx := t.Context()

	t.Run("restart_before_promotion_commit", func(t *testing.T) {
		f := tb04NewSQLFixture(t, map[string][]byte{
			"service.go": []byte("package svc\nconst State = \"before\"\n"),
		})
		f.seedPendingWorker(
			t, "tb04-w10-prom-before",
			[]string{"service.go"},
			map[string][]byte{"service.go": []byte("package svc\nconst State = \"after\"\n")},
			nil,
		)
		beforeHash := tb04HashTree(t, f.primary)

		// Simulate crash after filesystem apply, before CommitPromotion commits:
		// acquire apply lease, apply promoteTransaction to primary, expire lease, and restart without closing tx.
		token, ok, err := f.svcs.Promotions.BeginMergeApply(ctx, "tb04-w10-prom-before")
		testutil.FailErr(t, "BeginMergeApply", err)
		if !ok || token == "" {
			t.Fatal("expected merge apply claim")
		}
		task, ok := f.svcs.Queue.Get("tb04-w10-prom-before")
		if !ok {
			t.Fatal("missing job")
		}
		promote := PromoteRootsForTask(task, f.merge.taskRootRefs(ctx, task))
		tx, err := beginPromoteTransaction(f.merge.hostDataDirFor(task), task.ID, []promoteMutation{{
			path:           "service.go",
			original:       []byte("package svc\nconst State = \"before\"\n"),
			originalExists: true,
			originalMode:   0o644,
			target:         []byte("package svc\nconst State = \"after\"\n"),
			targetExists:   true,
		}})
		testutil.FailErr(t, "beginPromoteTransaction", err)
		testutil.FailErr(t, "tx.apply before crash", tx.apply(ctx, promote, task))
		_, err = f.sqlDB.ExecContext(ctx, `UPDATE worker_jobs SET merge_lease_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, task.ID)
		testutil.FailErr(t, "expire merge lease", err)

		// Restart: reopen DB and run startup recovery.
		f.reopen(t)
		testutil.FailErr(t, "RecoverOrphanedMergeApplies", f.merge.RecoverOrphanedMergeApplies(ctx))

		// Uncommitted promotion must have rolled back to exact pre-promotion tree and pending status.
		if got := tb04HashTree(t, f.primary); got != beforeHash {
			t.Fatalf("primary tree after pre-commit crash recovery = %s, want %s", got, beforeHash)
		}
		recoveredTask, ok := f.svcs.Queue.Get("tb04-w10-prom-before")
		if !ok || recoveredTask.MergeStatus != api.WorkerMergeStatusPending {
			t.Fatalf("task status after recovery = %+v, want pending", recoveredTask)
		}

		// Resume promotion cleanly after restart.
		out, err := f.merge.PromoteOverlay(ctx, f.parent, "tb04-w10-prom-before", api.PromoteOverlayInput{})
		testutil.FailErr(t, "resume PromoteOverlay", err)
		if out.Status != api.WorkerMergeStatusMerged {
			t.Fatalf("resumed promote status = %q, want merged", out.Status)
		}
		raw, err := os.ReadFile(filepath.Join(f.primary, "service.go"))
		testutil.FailErr(t, "read service.go", err)
		if string(raw) != "package svc\nconst State = \"after\"\n" {
			t.Fatalf("service.go = %q, want after", raw)
		}
	})

	t.Run("restart_after_promotion_commit_before_journal_close", func(t *testing.T) {
		f := tb04NewSQLFixture(t, map[string][]byte{
			"service.go": []byte("package svc\nconst State = \"before\"\n"),
		})
		f.seedPendingWorker(
			t, "tb04-w10-prom-after",
			[]string{"service.go"},
			map[string][]byte{"service.go": []byte("package svc\nconst State = \"after\"\n")},
			nil,
		)

		token, ok, err := f.svcs.Promotions.BeginMergeApply(ctx, "tb04-w10-prom-after")
		testutil.FailErr(t, "BeginMergeApply", err)
		if !ok {
			t.Fatal("expected merge apply claim")
		}
		task, ok := f.svcs.Queue.Get("tb04-w10-prom-after")
		if !ok {
			t.Fatal("missing job")
		}
		hostDataDir := f.merge.hostDataDirFor(task)
		promote := PromoteRootsForTask(task, f.merge.taskRootRefs(ctx, task))
		tx, err := beginPromoteTransaction(hostDataDir, task.ID, []promoteMutation{{
			path:           "service.go",
			original:       []byte("package svc\nconst State = \"before\"\n"),
			originalExists: true,
			originalMode:   0o644,
			target:         []byte("package svc\nconst State = \"after\"\n"),
			targetExists:   true,
		}})
		testutil.FailErr(t, "beginPromoteTransaction", err)
		testutil.FailErr(t, "tx.apply", tx.apply(ctx, promote, task))
		testutil.FailErr(t, "CommitPromotion", f.svcs.Promotions.CommitPromotion(ctx, task.ID, token, PromotionCommit{}))
		// Intentionally do NOT call tx.close() — simulate crash right after CommitPromotion.
		txDir := promoteTransactionDir(hostDataDir, task.ID)
		if _, err := os.Lstat(txDir); err != nil {
			t.Fatalf("expected unclosed transaction dir before restart: %v", err)
		}

		// Restart: boot recovery must settle the committed journal WITHOUT rolling back landed bytes.
		f.reopen(t)
		testutil.FailErr(t, "RecoverOrphanedMergeApplies", f.merge.RecoverOrphanedMergeApplies(ctx))
		if _, err := os.Lstat(txDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("committed transaction journal survived boot settlement: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(f.primary, "service.go"))
		testutil.FailErr(t, "read service.go after settlement", err)
		if string(raw) != "package svc\nconst State = \"after\"\n" {
			t.Fatalf("boot settlement rolled back committed bytes: %q", raw)
		}
		// Duplicate promote after restart must be refused (no duplicate landing).
		_, err = f.merge.PromoteOverlay(ctx, f.parent, "tb04-w10-prom-after", api.PromoteOverlayInput{})
		tb04RequireRejectCode(t, err, OverlayPromoteNotPendingCode)
	})
}

// Slot W10.5: Baseline manifest retention across retries, restart, pending promotion,
// branch cleanup, and worker deletion ref-count release check.
func TestWEATB04_W10_BaselineManifestRetentionAndDeletionRefCount(t *testing.T) {
	ctx := t.Context()
	f := tb04NewSQLFixture(t, map[string][]byte{
		"alpha.go": []byte("package alpha\nconst A = 1\n"),
		"beta.go":  []byte("package beta\nconst B = 2\n"),
	})
	jobID := "tb04-w10-baseline"
	_, err := f.svcs.Queue.Enqueue(ctx, api.WorkerTask{
		ID: jobID, ParentSessionID: f.parent, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: f.primary, AgentType: "implementer", Prompt: "retain baseline", Brief: "brief",
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"alpha.go", "beta.go"}},
	})
	testutil.FailErr(t, "enqueue", err)

	claimed1, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-1", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim 1", err)

	branch := filepath.Join(f.dataDir, "worker-branches", jobID)
	tb04CopyDirFiles(t, f.primary, branch)
	baselinePath := testbaseline.Durable(t, "root", f.sqlDB, jobID, f.primary)
	testbaseline.RecordBranch(t, "root", f.primary, branch)
	bound, err := f.svcs.Queue.store.SetWorkerWorkspace(ctx, jobID, branch, baselinePath)
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("SetWorkerWorkspace returned false")
	}
	baselineID, err := workspacebaseline.ID(baselinePath)
	testutil.FailErr(t, "baseline ID", err)

	var initialBlobRefs int
	testutil.FailErr(t, "count initial baseline objects", f.sqlDB.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM worker_baseline_objects WHERE baseline_id = ?`, baselineID,
	).Scan(&initialBlobRefs))
	if initialBlobRefs < 2 {
		t.Fatalf("initial worker_baseline_objects count = %d, want >= 2", initialBlobRefs)
	}

	assertBaselineIntact := func(stage string) {
		t.Helper()
		var exists int
		testutil.FailErr(t, "check worker_baselines at "+stage, f.sqlDB.QueryRowContext(
			ctx, `SELECT COUNT(*) FROM worker_baselines WHERE id = ?`, baselineID,
		).Scan(&exists))
		if exists != 1 {
			t.Fatalf("%s: worker_baselines row count = %d, want 1", stage, exists)
		}
		var refs int
		testutil.FailErr(t, "check worker_baseline_objects at "+stage, f.sqlDB.QueryRowContext(
			ctx, `SELECT COUNT(*) FROM worker_baseline_objects WHERE baseline_id = ?`, baselineID,
		).Scan(&refs))
		if refs != initialBlobRefs {
			t.Fatalf("%s: worker_baseline_objects count = %d, want %d", stage, refs, initialBlobRefs)
		}
		reader, err := workspacebaseline.Open(ctx, baselinePath, workspacebaseline.ContentStore(baselinePath))
		testutil.FailErr(t, "open baseline at "+stage, err)
		defer func() { _ = reader.Close() }()
		body, found, err := reader.Content(ctx, "alpha.go")
		testutil.FailErr(t, "read alpha.go from baseline at "+stage, err)
		if !found || body != "package alpha\nconst A = 1\n" {
			t.Fatalf("%s: baseline alpha.go = (%q, %v)", stage, body, found)
		}
	}

	// 1. Across retry: Retry returns job to pending; baseline stays intact.
	retried, err := f.svcs.Queue.Retry(ctx, claimed1, errors.New("transient provider timeout"))
	testutil.FailErr(t, "Retry", err)
	if !retried {
		t.Fatal("Retry did not succeed")
	}
	assertBaselineIntact("after_retry")

	// 2. Across restart: reopen SQLite DB and re-claim job; baseline stays intact.
	f.reopen(t)
	assertBaselineIntact("after_restart")

	claimed2, err := f.svcs.Queue.ClaimNext(ctx, ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller-2", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim 2 after restart", err)
	tb04WriteFiles(t, branch, map[string][]byte{"alpha.go": []byte("package alpha\nconst A = 2\n")})
	claimed2.WorkspaceRoot = branch
	claimed2.WorkspaceBaselinePath = baselinePath
	won, err := f.svcs.Queue.Complete(ctx, claimed2, api.WorkerResult{Status: "complete", Summary: "done after retry+restart"})
	testutil.FailErr(t, "Complete", err)
	if !won {
		t.Fatal("Complete did not commit")
	}
	assertBaselineIntact("pending_promotion")

	// 3. Across branch cleanup and aging/prune: remove branch tree, age baseline past orphanGrace,
	// and run Prune — baseline must STILL be retained while worker job exists.
	testutil.FailErr(t, "remove branch tree", os.RemoveAll(branch))
	oldTime := time.Now().Add(-2 * time.Hour)
	testutil.FailErr(t, "backdate manifest file", os.Chtimes(baselinePath, oldTime, oldTime))
	_, err = f.sqlDB.ExecContext(ctx, `UPDATE worker_baselines SET created_at = '2000-01-01T00:00:00Z' WHERE id = ?`, baselineID)
	testutil.FailErr(t, "backdate worker_baselines.created_at", err)

	baselineStore := workspacebaseline.New(
		f.sqlDB,
		sourceblob.New(filepath.Join(f.dataDir, "source-content")),
		filepath.Join(f.dataDir, "worker-baselines"),
	)
	defer func() { _ = baselineStore.Close() }()
	testutil.FailErr(t, "Prune while worker referenced", baselineStore.Prune(ctx))
	assertBaselineIntact("after_cleanup_and_prune")

	// 4. On worker deletion: deleting the worker row cascades to worker_baselines & worker_baseline_objects,
	// queues released blob refs in source_blob_reclaim_queue, and Prune removes the manifest file.
	_, err = f.sqlDB.ExecContext(ctx, `DELETE FROM worker_jobs WHERE id = ?`, jobID)
	testutil.FailErr(t, "delete worker job", err)

	var remainingBaselines, remainingObjRefs, queuedReclaims int
	testutil.FailErr(t, "count remaining baselines", f.sqlDB.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM worker_baselines WHERE id = ?`, baselineID,
	).Scan(&remainingBaselines))
	testutil.FailErr(t, "count remaining obj refs", f.sqlDB.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM worker_baseline_objects WHERE baseline_id = ?`, baselineID,
	).Scan(&remainingObjRefs))
	testutil.FailErr(t, "count reclaim queue", f.sqlDB.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM source_blob_reclaim_queue`,
	).Scan(&queuedReclaims))
	if remainingBaselines != 0 || remainingObjRefs != 0 {
		t.Fatalf("after worker deletion: baselines=%d obj_refs=%d, want 0/0", remainingBaselines, remainingObjRefs)
	}
	if queuedReclaims < initialBlobRefs {
		t.Fatalf("source_blob_reclaim_queue = %d, want >= %d released blob refs", queuedReclaims, initialBlobRefs)
	}

	// Prune uses a stateful ReadDir cursor: advance past EOF of the earlier sweep, then sweep again.
	testutil.FailErr(t, "Prune cursor wrap after worker deletion", baselineStore.Prune(ctx))
	testutil.FailErr(t, "Prune after worker deletion", baselineStore.Prune(ctx))
	if _, err := os.Stat(baselinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced baseline manifest survived Prune after worker deletion: %v", err)
	}
}
