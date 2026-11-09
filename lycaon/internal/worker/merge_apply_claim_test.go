package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBeginMergeApplyClaimsPendingOnce(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(sqlDB)
	q := NewSQLQueue(sqlDB, 8)
	dir := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, sqlDB, "parent-1", testdbseed.DefaultProjectID, dir)
	id, err := q.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Prompt:          "write",
		Brief:           "Update a.go",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}},
	})
	testutil.FailErr(t, "Enqueue", err)
	bound, err := store.SetWorkerWorkspace(ctx, id, filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", id), testbaseline.Durable(t, sqlDB, id, t.TempDir()))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("expected workspace root CAS to win on an unbound job")
	}
	testutil.FailErr(t, "SetMergeStatus", q.SetMergeStatus(ctx, id, api.WorkerMergeStatusPending))

	token, ok, err := q.BeginMergeApply(ctx, id)
	testutil.FailErr(t, "BeginMergeApply first", err)
	if !ok || token == "" {
		t.Fatalf("expected first claim with token, got ok=%v token=%q", ok, token)
	}
	_, ok, err = q.BeginMergeApply(ctx, id)
	testutil.FailErr(t, "BeginMergeApply second", err)
	if ok {
		t.Fatal("expected second claim to fail while applying")
	}
	task, found := q.Get(id)
	if !found || task.MergeStatus != api.WorkerMergeStatusApplying {
		t.Fatalf("status = %q found=%v", task.MergeStatus, found)
	}
}

func TestMergeApplyLeaseLossCancelsActiveWriter(t *testing.T) {
	store := obligationTestStore(t)
	oldToken := insertApplyingWorker(t, store, "job-lease-loss", t.TempDir())
	testutil.FailErr(t, "release setup claim", store.ReleaseMergeApply(t.Context(), "job-lease-loss", oldToken))

	previousInterval := mergeApplyHeartbeatInterval
	mergeApplyHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { mergeApplyHeartbeatInterval = previousInterval })
	service := &MergeService{Store: store}
	claim, claimCtx, err := service.beginMergeClaim(t.Context(), "job-lease-loss")
	testutil.FailErr(t, "begin merge claim", err)
	t.Cleanup(claim.stop)
	_, err = store.db.ExecContext(t.Context(), `
		UPDATE worker_jobs SET merge_claim_token = 'replacement-claim' WHERE id = 'job-lease-loss'`)
	testutil.FailErr(t, "replace merge claim", err)

	select {
	case <-claimCtx.Done():
		if !errors.Is(context.Cause(claimCtx), errMergeApplyClaimLost) {
			t.Fatalf("claim cause = %v", context.Cause(claimCtx))
		}
	case <-t.Context().Done():
		t.Fatal("active writer was not canceled after losing its merge lease")
	}
	if err := store.ReleaseMergeApply(t.Context(), "job-lease-loss", claim.token); !errors.Is(err, errMergeApplyClaimLost) {
		t.Fatalf("stale claim release = %v", err)
	}
}

func TestMergeServicePreviewAndPromoteFromPublishedCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		publishLater, failCommit bool
	}{
		{name: "reviewed checkpoint"},
		{name: "new checkpoint requires refreshed preview", publishLater: true},
		{name: "durable failure rolls back checkpoint effects", failCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publishLater := tc.publishLater

			ctx := t.Context()
			fixture := newPublishedCheckpointWorker(t)
			sqlDB, q, primary, jobID := fixture.db, fixture.queue, fixture.primary, fixture.jobID
			cpStore, core, lp, pub := fixture.store, fixture.core, fixture.path, fixture.publisher
			manifest, workerContent := fixture.manifest, fixture.workerContent
			featureOID := fixture.featureOID
			var err error

			// Trunk advances non-overlappingly on func A().
			trunkUpdated := "package app\n\nfunc A() int { return 10 }\n\nfunc B() int { return 2 }\n"
			testutil.FailErr(t, "write trunk update", os.WriteFile(filepath.Join(primary, "app.go"), []byte(trunkUpdated), 0o644))

			svc := &MergeService{Queue: q, Store: q, DataDir: t.TempDir()}
			previewOut, err := svc.PreviewForSession(ctx, "parent-cp", jobID, "summary", nil)
			testutil.FailErr(t, "PreviewForSession", err)
			if len(previewOut.CleanPaths) != 3 {
				t.Fatalf("preview CleanPaths = %v, want 3 clean paths", previewOut.CleanPaths)
			}

			// Mutate trunk after PreviewForSession to verify stale preview detection.
			trunkStale := "package app\n\nfunc A() int { return 99 }\n\nfunc B() int { return 2 }\n"
			testutil.FailErr(t, "write stale trunk", os.WriteFile(filepath.Join(primary, "app.go"), []byte(trunkStale), 0o644))
			if _, err := svc.PromoteOverlay(ctx, "parent-cp", jobID, api.PromoteOverlayInput{}); !errors.Is(err, checkpoint.ErrStalePreviewTargetChanged) {
				t.Fatalf("PromoteOverlay after target drift = %v, want ErrStalePreviewTargetChanged", err)
			}

			// Refresh preview against current trunk and promote cleanly without custom ref importer registration.
			_, err = svc.PreviewForSession(ctx, "parent-cp", jobID, "summary", nil)
			testutil.FailErr(t, "PreviewForSession refresh", err)
			if publishLater {
				laterContent := strings.Replace(workerContent, "return 22", "return 33", 1)
				_, err = core.WriteFile(lp, []byte(laterContent), 0o644)
				testutil.FailErr(t, "write later checkpoint content", err)
				_, err = pub.Publish(ctx, checkpoint.PublishRequest{
					CheckpointID: "chk-promote-later", Trigger: checkpoint.TriggerWorkerCompletion,
					PrivateGit: checkpoint.PrivateGitSnapshot{HeadRef: "refs/heads/feature", Refs: map[string]string{"refs/heads/feature": featureOID}},
				})
				testutil.FailErr(t, "publish checkpoint after reviewed preview", err)
				bindRecoveryCheckpoint(t, fixture.overlay, primary, fixture.store, "chk-promote-later")
			}
			if publishLater {
				_, err := svc.PromoteOverlay(ctx, "parent-cp", jobID, api.PromoteOverlayInput{})
				if !errors.Is(err, checkpoint.ErrStalePreviewCheckpointChanged) {
					t.Fatalf("superseded checkpoint promotion = %v", err)
				}
				body, err := os.ReadFile(filepath.Join(primary, "app.go"))
				testutil.FailErr(t, "read unchanged primary", err)
				if string(body) != trunkStale {
					t.Fatalf("superseded preview changed primary: %q", body)
				}
				if _, exists, err := cpStore.LoadPromotionRecord("tx-chk-promote-1"); err != nil || exists {
					t.Fatalf("superseded preview created journal: exists=%t err=%v", exists, err)
				}
				if task, ok := q.Get(jobID); !ok || task.MergeStatus != api.WorkerMergeStatusPending {
					t.Fatalf("superseded preview lost pending state: %+v", task)
				}
				if _, err := svc.PreviewForSession(ctx, "parent-cp", jobID, "summary", nil); err != nil {
					t.Fatalf("refresh superseded checkpoint preview: %v", err)
				}
				manifest, err = cpStore.LoadLatestManifest()
				testutil.FailErr(t, "load refreshed manifest", err)
			}
			if tc.failCommit {
				_, err := sqlDB.ExecContext(ctx, `CREATE TRIGGER reject_checkpoint_completion
					BEFORE UPDATE OF merge_status ON worker_jobs WHEN NEW.merge_status = 'merged'
					BEGIN SELECT RAISE(ABORT, 'fixture rejected checkpoint completion'); END`)
				testutil.FailErr(t, "install durable commit fault", err)
				_, err = svc.PromoteOverlay(ctx, "parent-cp", jobID, api.PromoteOverlayInput{})
				if err == nil {
					t.Fatal("promotion ignored durable commit failure")
				}
				body, readErr := os.ReadFile(filepath.Join(primary, "app.go"))
				testutil.FailErr(t, "read primary after failed commit", readErr)
				if string(body) != trunkStale {
					t.Fatalf("failed commit left promoted bytes: %q; error: %v", body, err)
				}
				appInfo, err := os.Lstat(filepath.Join(primary, "app.go"))
				testutil.FailErr(t, "lstat app.go after rollback", err)
				aliasInfo, err := os.Lstat(filepath.Join(primary, "app_alias.go"))
				testutil.FailErr(t, "lstat app_alias.go after rollback", err)
				if !os.SameFile(appInfo, aliasInfo) {
					t.Fatal("expected two-stage rollback after durable commit failure to restore hard-link identity between app.go and app_alias.go")
				}
				for _, rel := range []string{"run.sh", "link.txt", ".git/refs/lycaon/workers/" + jobID + "/heads/feature"} {
					if _, err := os.Lstat(filepath.Join(primary, rel)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed commit left checkpoint effect %s: %v", rel, err)
					}
				}
				if task, ok := q.Get(jobID); !ok || task.MergeStatus != api.WorkerMergeStatusPending {
					t.Fatalf("failed completion status=%+v", task)
				}
				if _, found, err := cpStore.LoadPromotionRecord("tx-" + manifest.CheckpointID); err != nil || found {
					t.Fatalf("failed completion retained component record: found=%t err=%v", found, err)
				}
				_, err = sqlDB.ExecContext(ctx, `DROP TRIGGER reject_checkpoint_completion`)
				testutil.FailErr(t, "remove completion fault", err)
			}
			promoteOut, err := svc.PromoteOverlay(ctx, "parent-cp", jobID, api.PromoteOverlayInput{})
			testutil.FailErr(t, "PromoteOverlay", err)
			wantStatus := api.WorkerMergeStatusMerged
			if promoteOut.Status != wantStatus {
				t.Fatalf("PromoteOverlay status = %q, want %q", promoteOut.Status, wantStatus)
			}

			mergedBytes, err := os.ReadFile(filepath.Join(primary, "app.go"))
			testutil.FailErr(t, "ReadFile merged app.go", err)
			wantMerged := "package app\n\nfunc A() int { return 99 }\n\nfunc B() int { return 22 }\n"
			if publishLater {
				wantMerged = strings.Replace(wantMerged, "return 22", "return 33", 1)
			}
			if string(mergedBytes) != wantMerged {
				t.Fatalf("merged app.go =\n%s\nwant:\n%s", string(mergedBytes), wantMerged)
			}

			scriptInfo, err := os.Stat(filepath.Join(primary, "run.sh"))
			testutil.FailErr(t, "Stat run.sh", err)
			if scriptInfo.Mode().Perm() != 0o755 {
				t.Fatalf("run.sh mode = %#o, want 0o755", scriptInfo.Mode().Perm())
			}

			linkTarget, err := os.Readlink(filepath.Join(primary, "link.txt"))
			testutil.FailErr(t, "Readlink link.txt", err)
			if linkTarget != "app.go" {
				t.Fatalf("link.txt target = %q, want app.go", linkTarget)
			}

			gitRefBytes, err := os.ReadFile(filepath.Join(primary, ".git", "refs", "lycaon", "workers", jobID, "heads", "feature"))
			testutil.FailErr(t, "ReadFile imported git ref", err)
			if strings.TrimSpace(string(gitRefBytes)) != featureOID {
				t.Fatalf("imported git ref file = %q, want %s", string(gitRefBytes), featureOID)
			}
			cpRefBytes, err := os.ReadFile(filepath.Join(primary, ".git", "refs", "lycaon", "checkpoints", manifest.CheckpointID, "heads", "feature"))
			testutil.FailErr(t, "ReadFile imported checkpoint ref", err)
			if strings.TrimSpace(string(cpRefBytes)) != featureOID {
				t.Fatalf("imported checkpoint ref file = %q, want %s", string(cpRefBytes), featureOID)
			}

			rec, exists, err := cpStore.LoadPromotionRecord("tx-" + manifest.CheckpointID)
			testutil.FailErr(t, "LoadPromotionRecord", err)
			if !exists || rec.State != checkpoint.PromotionStateApplied {
				t.Fatalf("promotion journal record exists=%v state=%q", exists, rec.State)
			}
			if rec.FilePromotionStatus != checkpoint.EffectCompleted || rec.RefImportStatus != checkpoint.EffectCompleted {
				t.Fatalf("effects file=%q ref=%q", rec.FilePromotionStatus, rec.RefImportStatus)
			}
			if rec.ImportedRefs["refs/lycaon/workers/"+jobID+"/heads/feature"] != featureOID || rec.ImportedRefs["refs/lycaon/checkpoints/"+manifest.CheckpointID+"/heads/feature"] != featureOID {
				t.Fatalf("ImportedRefs = %v", rec.ImportedRefs)
			}

		})
	}
}

// publishedCheckpointWorker owns a completed worker and its immutable checkpoint;
// the promotion tests can advance trunk, refresh review, and inject commit faults.
type publishedCheckpointWorker struct {
	db                                        *db.Store
	queue                                     *SQLQueue
	primary, jobID, workerContent, featureOID string
	overlay                                   string
	store                                     *checkpoint.Store
	core                                      *layeredfs.Core
	path                                      layeredfs.QualifiedPath
	publisher                                 *checkpoint.Publisher
	manifest                                  checkpoint.Manifest
}

func newPublishedCheckpointWorker(t *testing.T) publishedCheckpointWorker {
	t.Helper()
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "store.db")
	sqlStore := NewSQLStore(sqlDB)
	q := NewSQLQueue(sqlDB, 8)
	primary := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, sqlDB, "parent-cp", testdbseed.DefaultProjectID, primary)

	baseContent := "package app\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n"
	testutil.FailErr(t, "write primary base", os.WriteFile(filepath.Join(primary, "app.go"), []byte(baseContent), 0o644))
	testutil.FailErr(t, "link primary alias", os.Link(filepath.Join(primary, "app.go"), filepath.Join(primary, "app_alias.go")))

	jobID, err := q.Enqueue(ctx, api.WorkerTask{
		ID:              "job-cp-promote",
		ParentSessionID: "parent-cp",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   primary,
		AgentType:       "implementer",
		Prompt:          "update B",
		Brief:           "Update B",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite},
	})
	testutil.FailErr(t, "Enqueue", err)
	t.Cleanup(func() { session.ClearTaskCheckpointPreview(jobID) })

	overlay := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", jobID)
	testutil.FailErr(t, "MkdirAll overlay", os.MkdirAll(overlay, 0o755))
	baselinePath := testbaseline.Durable(t, sqlDB, jobID, primary)
	cpStore := openRecoveryCheckpointStore(t)
	cpStoreDir := cpStore.RootDir()

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-cp-promote",
		BackingDir: t.TempDir(),
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })

	lp := layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}
	workerContent := "package app\n\nfunc A() int { return 1 }\n\nfunc B() int { return 22 }\n"
	_, err = core.WriteFile(lp, []byte(workerContent), 0o644)
	testutil.FailErr(t, "WriteFile worker", err)

	scriptLP := layeredfs.QualifiedPath{RootID: "primary", Rel: "run.sh"}
	_, err = core.WriteFile(scriptLP, []byte("#!/bin/sh\necho ok\n"), 0o755)
	testutil.FailErr(t, "WriteFile run.sh", err)

	linkLP := layeredfs.QualifiedPath{RootID: "primary", Rel: "link.txt"}
	_, err = core.Symlink("app.go", linkLP)
	testutil.FailErr(t, "Symlink link.txt", err)

	testutil.FailErr(t, "MkdirAll primary .git/objects", os.MkdirAll(filepath.Join(primary, ".git", "objects"), 0o755))
	featureOID, err := checkpoint.WriteLooseGitObject(filepath.Join(cpStoreDir, "objects"), "blob", []byte("feature-snapshot\n"))
	testutil.FailErr(t, "WriteLooseGitObject", err)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		WorkerID:         jobID,
		TaskID:           jobID,
		ProjectID:        testdbseed.DefaultProjectID,
		SourceGeneration: "gen-1",
		Core:             core,
		Store:            cpStore,
	})
	testutil.FailErr(t, "NewPublisher", err)

	pubRes, err := pub.Publish(ctx, checkpoint.PublishRequest{
		CheckpointID: "chk-promote-1",
		Trigger:      checkpoint.TriggerWorkerCompletion,
		PrivateGit: checkpoint.PrivateGitSnapshot{
			HeadRef: "refs/heads/feature",
			Refs:    map[string]string{"refs/heads/feature": featureOID},
		},
	})
	testutil.FailErr(t, "Publish", err)
	manifest := pubRes.Manifest
	bindRecoveryCheckpoint(t, overlay, primary, cpStore, manifest.CheckpointID)

	_, err = sqlStore.SetWorkerWorkspace(ctx, jobID, overlay, baselinePath)
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	testutil.FailErr(t, "SetMergeStatus", q.SetMergeStatus(ctx, jobID, api.WorkerMergeStatusPending))

	// Tear down the active worker view directory completely after checkpoint publication.
	testutil.FailErr(t, "RemoveAll overlay view", os.RemoveAll(overlay))

	return publishedCheckpointWorker{overlay: overlay, db: sqlDB, queue: q, primary: primary, jobID: jobID, workerContent: workerContent, featureOID: featureOID, store: cpStore, core: core, path: lp, publisher: pub, manifest: manifest}
}
