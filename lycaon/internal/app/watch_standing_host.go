package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) watchStore() *watch.Store {
	if b == nil || b.server.Coordinator == nil {
		return nil
	}
	return b.server.Coordinator.CoordinatorLoop().Waits.Subscriptions.WatchStore()
}

func wireStandingWatchHost(b *serveBuilder, watchStore *watch.Store) {
	if b == nil || watchStore == nil || watchStore.Standing() == nil {
		return
	}
	engine := watchStore.Standing()
	if b.sessions != nil && b.sessions.Manager != nil {
		mgr := b.sessions.Manager
		if mgr.Limits != nil {
			mgr.Limits.SetRunSpendLookup(engine.ActiveRunSpendLimit)
		}
		if mgr.Chats != nil {
			mgr.Chats.SetOnSessionUnavailable(func(ctx context.Context, sessionID string) {
				_ = engine.NotifySessionClosedOrDeleted(ctx, sessionID)
			})
		}
	}

	var wg sync.WaitGroup
	engine.SetCallbacks(watch.StandingCallbacks{
		ModelPriceCheck: func(ctx context.Context, projectID, modelID string) (bool, error) {
			return standingModelPriceCheck(b, ctx, projectID, modelID)
		},
		PersonAccessCheck: func(ctx context.Context, projectID, personID string) (bool, error) {
			return standingPersonAccessCheck(b, ctx, projectID, personID)
		},
		SessionTargetCheck: func(ctx context.Context, projectID, sessionID string) (bool, bool, error) {
			return standingSessionTargetCheck(b, ctx, projectID, sessionID)
		},
		ProjectDefaults: func(ctx context.Context, projectID string) (string, string, string, watch.Authority) {
			return standingProjectDefaults(b, ctx, projectID)
		},
		RunDispatcher: func(ctx context.Context, sw watch.StandingWatch, run watch.WatchRun, promptText string) (watch.StandingRunDispatchOutcome, error) {
			return dispatchStandingWatchRun(b, ctx, &wg, engine, sw, run, promptText)
		},
	})

	var stopCh chan struct{}
	var unsub func()
	if b.events != nil && b.events.Hub != nil {
		ch, u, err := b.events.Hub.Subscribe(b.startup.ctx, events.Subscription{
			Viewer: people.Person{ID: "standing-watch-host", Role: api.PersonRoleOwner},
		})
		if err == nil {
			stopCh = make(chan struct{})
			unsub = u
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stopCh:
						return
					case env, ok := <-ch:
						if !ok {
							return
						}
						observeStandingWatchEvent(b.startup.ctx, b, engine, env)
					}
				}
			}()
		}
	}
	if b.startup.resources != nil {
		b.startup.resources.Track("standing-watch-events", 89, func(context.Context) error {
			if stopCh != nil {
				close(stopCh)
			}
			if unsub != nil {
				unsub()
			}
			wg.Wait()
			return nil
		})
	}
}

func observeStandingWatchEvent(ctx context.Context, b *serveBuilder, engine *watch.StandingEngine, env api.EventEnvelope) {
	if env.Topic == api.EventTopicCheckpoint {
		var ev api.CheckpointEvent
		if json.Unmarshal(env.Data, &ev) != nil || strings.TrimSpace(ev.SessionID) == "" {
			return
		}
		if ev.Status == api.CheckpointStatusPending {
			_ = engine.MarkWaitingForOwner(ctx, ev.SessionID)
		} else {
			_ = engine.MarkResumed(ctx, ev.SessionID)
		}
		return
	}
	if env.Topic == api.EventTopicSession {
		var ev api.SessionEvent
		if json.Unmarshal(env.Data, &ev) != nil || strings.TrimSpace(ev.ID) == "" {
			return
		}
		if ev.Action == api.SessionEventActionDeleted {
			_ = engine.NotifySessionClosedOrDeleted(ctx, ev.ID)
			return
		}
		if ev.Status == api.SessionStatusIdle && !sessionHasOpenCheckpoint(b, ctx, ev.ID) {
			_ = engine.SettleRun(ctx, ev.ID, watch.RunCompleted)
		}
	}
}

func sessionHasOpenCheckpoint(b *serveBuilder, ctx context.Context, sessionID string) bool {
	if b.storage.Database == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	var count int
	err := b.storage.Database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM checkpoints WHERE session_id = ? AND status = 'pending'`,
		strings.TrimSpace(sessionID)).Scan(&count)
	return err == nil && count > 0
}

func standingModelPriceCheck(b *serveBuilder, ctx context.Context, _, modelID string) (bool, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || b.providers.Costs == nil {
		return true, nil
	}
	providerID, model := splitProviderModelID(modelID)
	if est, estErr := b.providers.Costs.Estimate(ctx, providerID, model, cost.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000}); estErr == nil {
		return !est.Unpriced, nil
	}
	return false, nil
}

func splitProviderModelID(modelID string) (string, string) {
	if p, m, ok := strings.Cut(strings.TrimSpace(modelID), ":"); ok && p != "" && m != "" {
		return p, m
	}
	return "", strings.TrimSpace(modelID)
}

func standingPersonAccessCheck(b *serveBuilder, ctx context.Context, _, personID string) (bool, error) {
	if b.storage.Database == nil || strings.TrimSpace(personID) == "" {
		return false, nil
	}
	var role string
	err := b.storage.Database.QueryRowContext(ctx,
		`SELECT role FROM people WHERE id = ?`, strings.TrimSpace(personID)).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return role == string(api.PersonRoleOwner), nil
}

func standingSessionTargetCheck(b *serveBuilder, ctx context.Context, projectID, sessionID string) (bool, bool, error) {
	if b.storage.Sessions == nil || b.storage.Sessions.Sessions == nil {
		return false, false, nil
	}
	sess, getErr := b.storage.Sessions.Sessions.Get(ctx, strings.TrimSpace(sessionID))
	if getErr == nil && sess != nil && sess.ProjectID == strings.TrimSpace(projectID) && sess.ArchivedAt == nil && !sess.IsWorkerChild() {
		if b.sessions != nil && b.sessions.Manager != nil && b.sessions.Manager.Submissions != nil {
			eligible, _ := b.sessions.Manager.Submissions.AmbientWakeEligible(ctx, sess.ID)
			return true, !eligible, nil
		}
		return true, sess.Status != api.SessionStatusIdle, nil
	}
	return false, false, nil
}

func standingProjectDefaults(b *serveBuilder, ctx context.Context, projectID string) (string, string, string, watch.Authority) {
	tz := time.Local.String()
	if tz == "" || strings.EqualFold(tz, "Local") {
		tz = "UTC"
	}
	auth := loadStandingProjectAuthority(b, ctx, projectID)
	modelID := ""
	if b.providers.Service != nil && len(auth.RootPaths) > 0 {
		if policy, err := b.providers.Service.Policy.GetForProjectRoots(auth.RootPaths); err == nil {
			if policy.Coordinator.ProviderID != "" && policy.Coordinator.Model != "" {
				modelID = policy.Coordinator.ProviderID + ":" + policy.Coordinator.Model
			} else if policy.Coordinator.Model != "" {
				modelID = policy.Coordinator.Model
			}
		}
	}
	return "default", modelID, tz, auth
}

func loadStandingProjectAuthority(b *serveBuilder, ctx context.Context, projectID string) watch.Authority {
	if b.storage.Database == nil {
		return watch.Authority{}
	}
	rows, err := b.storage.Database.QueryContext(ctx,
		`SELECT id, label, path FROM project_roots WHERE project_id = ? ORDER BY created_at ASC`,
		strings.TrimSpace(projectID))
	if err != nil {
		return watch.Authority{}
	}
	defer func() { _ = rows.Close() }()
	var auth watch.Authority
	for rows.Next() {
		var id, label, p string
		if rows.Scan(&id, &label, &p) == nil {
			clean := strings.TrimPrefix(strings.TrimSpace(label), "@")
			if clean == "" {
				clean = id
			}
			if auth.ProjectDir == "" {
				auth.ProjectDir = p
			}
			auth.RootPaths = append(auth.RootPaths, p)
			auth.Roots = append(auth.Roots, watch.WatchRootGrant{Label: clean, RootID: id, Path: p})
		}
	}
	if rows.Err() != nil {
		return watch.Authority{}
	}
	return auth
}

func dispatchStandingWatchRun(
	b *serveBuilder,
	ctx context.Context,
	wg *sync.WaitGroup,
	engine *watch.StandingEngine,
	sw watch.StandingWatch,
	run watch.WatchRun,
	promptText string,
) (watch.StandingRunDispatchOutcome, error) {
	if b.sessions == nil || b.sessions.Manager == nil || b.sessions.Manager.Submissions == nil || b.storage.Sessions == nil {
		return watch.StandingRunDispatchOutcome{}, nil
	}
	providerID, model := splitProviderModelID(sw.ModelID)
	sessionID := strings.TrimSpace(run.SessionID)
	if sw.Target.Kind == "new_chat" {
		if sessionID == "" {
			ownerCtx := people.WithCaller(ctx, people.Person{ID: sw.OwnerPersonID, Role: api.PersonRoleOwner})
			sess, err := b.storage.Sessions.Sessions.Create(ownerCtx, api.CreateSessionRequest{
				ProjectID:  sw.ProjectID,
				Posture:    api.SessionPostureBuild,
				ProviderID: providerID,
				Model:      model,
			}, sw.ProjectID)
			if err != nil {
				return watch.StandingRunDispatchOutcome{}, err
			}
			sessionID = sess.ID
			_ = engine.RecordChatProvenance(ctx, watch.StandingChatProvenance{
				SessionID: sessionID,
				ProjectID: sw.ProjectID,
				WatchID:   sw.WatchID,
				WatchName: sw.Name,
				RunID:     run.RunID,
			})
		}
	} else {
		sessionID = strings.TrimSpace(sw.Target.SessionID)
		eligible, retryable := b.sessions.Manager.Submissions.AmbientWakeEligible(ctx, sessionID)
		if !eligible {
			if retryable {
				return watch.StandingRunDispatchOutcome{SessionID: sessionID, BusyRetry: true}, nil
			}
			return watch.StandingRunDispatchOutcome{SessionID: sessionID}, errors.New("target chat is not eligible for automatic admission")
		}
	}

	in := promptinput.Input{
		SubmissionID:   run.RunID,
		AuthorPersonID: sw.OwnerPersonID,
		Text:           promptText,
		ProseFinish:    true,
		HostSignal: &promptinput.HostSignal{
			Kind:              api.MessageKindHostNudge,
			ID:                "standing_watch_run:" + run.RunID,
			TranscriptVisible: true,
		},
	}
	runCtx := people.WithoutCaller(context.WithoutCancel(ctx))
	wg.Add(1)
	go func(sid string) {
		defer wg.Done()
		resp, runErr := b.sessions.Manager.Submissions.HostTurnWithAdmission(runCtx, sid, sessionstore.PromptSubmissionOriginLoopWake, in, nil)
		switch {
		case errors.Is(runErr, spendguard.ErrCeiling) || (resp != nil && resp.Outcome == promptresult.OutcomeSpendLimit):
			_ = engine.SettleRun(runCtx, sid, watch.RunFenced)
		case errors.Is(runErr, lifecycle.ErrStopping):
			_ = engine.SettleRun(runCtx, sid, watch.RunCanceled)
		case runErr != nil:
			_ = engine.SettleRun(runCtx, sid, watch.RunFailed)
		case sessionHasOpenCheckpoint(b, runCtx, sid):
			_ = engine.MarkWaitingForOwner(runCtx, sid)
		default:
			_ = engine.SettleRun(runCtx, sid, watch.RunCompleted)
		}
	}(sessionID)
	return watch.StandingRunDispatchOutcome{SessionID: sessionID}, nil
}
