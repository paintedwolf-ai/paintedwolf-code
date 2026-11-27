package app

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/repochange"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func wireWatchProducers(b *serveBuilder, watchStore *watch.Store) {
	if b == nil || watchStore == nil {
		return
	}
	watchStore.SetAuthorityChecker(func(ctx context.Context, reg watch.Registration) (bool, string) {
		return checkWatchAuthority(b, ctx, reg)
	})
	watchStore.SetCoordinatorStateLookup(func(ctx context.Context, sessionID string) (watch.WorkerWatchStateSnapshot, error) {
		return lookupCoordinatorWatchState(b, ctx, sessionID)
	})

	unsubRepo := repochange.RegisterObserver(func(ctx context.Context, ev repochange.Event) {
		if ev.Kind == repochange.HeadMoved || ev.Kind == repochange.WorktreeChanged {
			_ = watchStore.NotifyWorkspaceChange(ctx, "", ev.ProjectDir)
		}
	})
	unsubSource := sourcefeed.SubscribeAll(func(projectID, _ string, _ sourcefeed.Notice) {
		_ = watchStore.NotifyWorkspaceChange(b.startup.ctx, projectID, "")
	})
	if b.startup.resources != nil {
		b.startup.resources.Track("watch-source-observers", 90, func(context.Context) error {
			unsubRepo()
			unsubSource()
			return nil
		})
	}

	if b.sessions != nil && b.sessions.Decisions != nil {
		if sqlDec, ok := b.sessions.Decisions.(*sessiondecisions.SQL); ok && sqlDec != nil {
			sqlDec.SetWatchHooks(
				func(ctx context.Context, dec api.WorkerDecisionRequest) {
					parentID, workerName := resolveWorkerParentAndName(b, ctx, dec.WorkerID, dec.ChildSessionID)
					if parentID != "" {
						_, _ = watchStore.RecordWorkerDecisionOpened(ctx, parentID, workerName, dec.WorkerID, dec)
					}
				},
				func(ctx context.Context, childSessionID string) {
					parentID, workerName, jobID := resolveChildWorkerIdentity(b, ctx, childSessionID)
					if parentID != "" {
						_ = watchStore.RecordWorkerDecisionClosed(ctx, parentID, workerName, jobID, "closed")
					}
				},
			)
		}
	}
	if b.boards != nil && b.boards.AnswerDecision != nil {
		if sqlRes, ok := b.boards.AnswerDecision.Resolver.(*worker.SQLDecisionResolver); ok && sqlRes != nil {
			sqlRes.SetOnResolved(func(ctx context.Context, dec api.WorkerDecisionRequest) {
				parentID, workerName := resolveWorkerParentAndName(b, ctx, dec.WorkerID, dec.ChildSessionID)
				if parentID != "" {
					_ = watchStore.RecordWorkerDecisionClosed(ctx, parentID, workerName, dec.WorkerID, "closed")
				}
			})
		}
	}
	if b.sessions != nil && b.sessions.Manager != nil {
		mgr := b.sessions.Manager
		if mgr.Coordinator.Workers != nil {
			mgr.Coordinator.Workers.OnWatchTerminal = func(ctx context.Context, parentID, _ string) {
				idle, overlay := coordinatorLevelFacts(b, ctx, parentID)
				_, _ = watchStore.RecordWorkerEvent(ctx, parentID, watch.WorkerWatchEvent{
					Kind:                  "state",
					AllWorkersIdle:        idle,
					OverlayPromotePending: overlay,
				})
			}
			mgr.Coordinator.Workers.OnWatchRunning = func(ctx context.Context, parentID, jobID, workerName, kind, outcome, occID, summary string) {
				idle, overlay := coordinatorLevelFacts(b, ctx, parentID)
				_, _ = watchStore.RecordWorkerEvent(ctx, parentID, watch.WorkerWatchEvent{
					Kind:                  kind,
					WorkerHandle:          workerName,
					JobID:                 jobID,
					Outcome:               outcome,
					OccurrenceID:          occID,
					Summary:               summary,
					AllWorkersIdle:        idle,
					OverlayPromotePending: overlay,
				})
			}
		}
		if mgr.Coordinator.Scans != nil {
			mgr.Coordinator.Scans.WatchSessions = watchStore.LiveScanWatchSessions
			mgr.Coordinator.Scans.OnWatchScanFinished = func(ctx context.Context, sessionID string, scan api.CodeScan) bool {
				var cats []string
				for _, c := range scan.Categories {
					if s := strings.TrimSpace(string(c)); s != "" {
						cats = append(cats, s)
					}
				}
				claimed, _ := watchStore.RecordScanDone(ctx, sessionID, watch.ScanDoneFact{
					ScanID:        scan.ID,
					Status:        string(scan.Status),
					Categories:    strings.Join(cats, ", "),
					FindingsCount: scan.FindingsCount,
				})
				return claimed
			}
		}
	}
	if b.workerRuntime != nil && b.workerRuntime.Cancel != nil {
		b.workerRuntime.Cancel.OnJobRetired = func(ctx context.Context, childSessionID, jobID string) {
			if strings.TrimSpace(childSessionID) != "" && strings.TrimSpace(jobID) != "" {
				_ = watchStore.RetireOwner(ctx, watch.JobOwner(childSessionID, jobID))
			}
		}
	}
}

func wireProcessOutputWatchProducer(b *serveBuilder, watchStore *watch.Store) {
	if b == nil || watchStore == nil || b.sessions == nil || b.sessions.Manager == nil || b.sessions.Manager.Processes == nil || b.sessions.Manager.Processes.Background == nil {
		return
	}
	bg := b.sessions.Manager.Processes.Background
	watchStore.SetProcessOutputLookup(func(ctx context.Context, sessionID, handle string, since int64) (bgprocess.ProcessOutputState, error) {
		return bg.Output.InspectProcessOutput(ctx, sessionID, handle, since)
	})
	bg.Output.SetWatchOutputHook(b.startup.ctx, func(ctx context.Context, sessionID, handle string) {
		_ = watchStore.NotifyProcessOutput(ctx, sessionID, handle)
	})
}

func bindWorkerOutcomeWatches(b *serveBuilder, bridge *worker.SessionOutcomeBridge) {
	if b == nil || bridge == nil || b.server.Coordinator == nil {
		return
	}
	watchStore := b.server.Coordinator.CoordinatorLoop().Waits.Subscriptions.WatchStore()
	if watchStore == nil {
		return
	}
	bridge.OnJobRetired = func(ctx context.Context, childSessionID, jobID string) {
		if strings.TrimSpace(childSessionID) != "" && strings.TrimSpace(jobID) != "" {
			_ = watchStore.RetireOwner(ctx, watch.JobOwner(childSessionID, jobID))
		}
	}
	bridge.OnWorkerWatchTerminal = func(ctx context.Context, parentSessionID, jobID, status, summary string) bool {
		_, workerName := resolveWorkerParentAndName(b, ctx, jobID, "")
		outcome := "satisfied"
		switch status {
		case "failed":
			outcome = "failed"
		case "needs_decision":
			outcome = "needs_decision"
		}
		idle, overlay := coordinatorLevelFacts(b, ctx, parentSessionID)
		claimed, _ := watchStore.RecordWorkerEvent(ctx, parentSessionID, watch.WorkerWatchEvent{
			Kind:                  "terminal",
			WorkerHandle:          workerName,
			JobID:                 jobID,
			Outcome:               outcome,
			Status:                status,
			Summary:               summary,
			OccurrenceID:          "worker_terminal:" + strings.TrimSpace(jobID),
			AllWorkersIdle:        idle,
			OverlayPromotePending: overlay,
		})
		return claimed
	}
}

func checkWatchAuthority(b *serveBuilder, ctx context.Context, reg watch.Registration) (bool, string) {
	if reg.Owner.Kind == watch.OwnerPerson {
		return true, ""
	}
	if b.storage.Sessions == nil || b.storage.Sessions.Sessions == nil {
		return true, ""
	}
	sess, err := b.storage.Sessions.Sessions.Get(ctx, reg.Owner.SessionID)
	if err != nil || sess == nil {
		return false, "SESSION_UNAVAILABLE"
	}
	if reg.Owner.ProjectID != "" && sess.ProjectID != "" && sess.ProjectID != reg.Owner.ProjectID {
		return false, "PROJECT_MISMATCH"
	}
	return true, ""
}

func lookupCoordinatorWatchState(b *serveBuilder, ctx context.Context, sessionID string) (watch.WorkerWatchStateSnapshot, error) {
	var snap watch.WorkerWatchStateSnapshot
	idle, overlay := coordinatorLevelFacts(b, ctx, sessionID)
	snap.AllWorkersIdle = idle
	snap.OverlayPromotePending = overlay

	if b.storage.Database != nil {
		jobs, err := queryCoordinatorWorkerJobs(ctx, b, sessionID)
		if err == nil {
			snap.HasWorkerCycle = len(jobs) > 0
			for _, j := range jobs {
				_, workerName := resolveWorkerParentAndName(b, ctx, j.id, j.child)
				if j.child != "" && b.sessions != nil && b.sessions.Decisions != nil {
					if dec, ok, dErr := b.sessions.Decisions.Get(ctx, j.child); dErr == nil && ok && dec.WorkerID == j.id {
						snap.OpenDecisions = append(snap.OpenDecisions, watch.OpenWorkerDecisionFact{
							WorkerHandle: workerName,
							JobID:        j.id,
							Request:      dec,
						})
					}
				}
				switch j.status {
				case "completed", "complete", "failed", "partial":
					st := j.status
					if st == "completed" {
						st = "complete"
					}
					snap.SettledWorkers = append(snap.SettledWorkers, watch.SettledWorkerFact{
						WorkerHandle: workerName,
						JobID:        j.id,
						Status:       st,
					})
				}
			}
		}
	}
	if b.scanning != nil && b.scanning.Store != nil && b.scanning.Store.Reads != nil {
		if scans, sErr := b.scanning.Store.Reads.ListBySessionID(ctx, sessionID); sErr == nil {
			for _, sc := range scans {
				if sc.Status != "" && sc.Status != api.CodeScanStatusPending && sc.Status != api.CodeScanStatusRunning {
					var cats []string
					for _, c := range sc.Categories {
						if s := strings.TrimSpace(string(c)); s != "" {
							cats = append(cats, s)
						}
					}
					snap.CompletedScans = append(snap.CompletedScans, watch.ScanDoneFact{
						ScanID:        sc.ID,
						Status:        string(sc.Status),
						Categories:    strings.Join(cats, ", "),
						FindingsCount: sc.FindingsCount,
					})
				}
			}
		}
	}
	return snap, nil
}

type coordinatorWorkerJobRow struct {
	id, child, status string
}

func queryCoordinatorWorkerJobs(ctx context.Context, b *serveBuilder, sessionID string) ([]coordinatorWorkerJobRow, error) {
	rows, err := b.storage.Database.QueryContext(ctx,
		`SELECT id, COALESCE(child_session_id, ''), status FROM worker_jobs WHERE parent_session_id = ? ORDER BY created_at DESC LIMIT 64`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var jobs []coordinatorWorkerJobRow
	for rows.Next() {
		var r coordinatorWorkerJobRow
		if err := rows.Scan(&r.id, &r.child, &r.status); err == nil {
			jobs = append(jobs, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func coordinatorLevelFacts(b *serveBuilder, ctx context.Context, sessionID string) (allIdle, overlayPending bool) {
	if b.server.Coordinator == nil {
		return false, false
	}
	loop := b.server.Coordinator.CoordinatorLoop()
	allIdle = loop.Cycles.WorkerCycleIsIdle(ctx, sessionID)
	overlayPending = loop.Waits.Subscriptions.OverlayPromoteDue(ctx, sessionID)
	return allIdle, overlayPending
}

func resolveWorkerParentAndName(b *serveBuilder, ctx context.Context, jobID, childSessionID string) (string, string) {
	var parentID, workerName string
	if b.workerRuntime != nil && b.workerRuntime.Queue != nil && strings.TrimSpace(jobID) != "" {
		if task, ok := b.workerRuntime.Queue.Lookup(ctx, jobID); ok && task != nil {
			parentID = strings.TrimSpace(task.ParentSessionID)
		}
	}
	if parentID == "" && strings.TrimSpace(childSessionID) != "" && b.storage.Sessions != nil && b.storage.Sessions.Sessions != nil {
		if sess, err := b.storage.Sessions.Sessions.Get(ctx, childSessionID); err == nil && sess != nil {
			parentID = strings.TrimSpace(sess.ParentSessionID)
		}
	}
	if b.boards != nil && b.boards.WorkerRecords != nil && strings.TrimSpace(jobID) != "" {
		if names, ok, err := b.boards.WorkerRecords.NamesByJob(ctx, jobID); err == nil && ok {
			workerName = strings.TrimSpace(names.Worker)
		}
	}
	return parentID, workerName
}

func resolveChildWorkerIdentity(b *serveBuilder, ctx context.Context, childSessionID string) (string, string, string) {
	var jobID string
	if b.storage.Database != nil && strings.TrimSpace(childSessionID) != "" {
		_ = b.storage.Database.QueryRowContext(ctx,
			`SELECT id FROM worker_jobs WHERE child_session_id = ? ORDER BY created_at DESC LIMIT 1`,
			strings.TrimSpace(childSessionID)).Scan(&jobID)
	}
	parentID, workerName := resolveWorkerParentAndName(b, ctx, jobID, childSessionID)
	return parentID, workerName, jobID
}
