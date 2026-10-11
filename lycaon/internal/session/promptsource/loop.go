package promptsource

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/submissions"
	"github.com/lycaon/lycaon/internal/session/turnadmission"
	"github.com/lycaon/lycaon/internal/session/turnsettlement"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

type LoopRepository interface {
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
}
type Grounding interface{ IsEscalated(string) bool }
type ActiveRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
}
type Loop struct {
	ActiveRuns   ActiveRuns
	Admission    *turnadmission.Service
	Events       *events.Publisher
	Frame        inject.CoordinatorTurnFrameSource
	Grounding    Grounding
	Guidance     *guidancedelivery.Service
	Limits       *sessionlimits.Service
	Processes    *processcontrol.Service
	Runtime      *coordinator.Runtime
	ScanInFlight func(context.Context, string) bool
	Sessions     LoopRepository
	Settlement   *turnsettlement.Service
	State        *workeroutcomes.State
	Submissions  *submissions.Service
	Turns        *execution.Journal
	Workers      workeroutcomes.CycleLedger
	Workflow     *loopwake.WorkflowDomains
}

func (m *Loop) Build() loopwake.LoopDeps {
	return loopwake.LoopDeps{
		RunPrompt: func(ctx context.Context, sessionID string) (*promptresult.Result, error) {
			return m.Submissions.LoopWake(ctx, sessionID)
		},
		RunWaitResume:   m.Submissions.WaitResume,
		HostTurnBlocked: m.Turns.HostTurnBlocked,
		GetSession: func(ctx context.Context, sessionID string) (*api.Session, error) {
			return m.Sessions.Get(ctx, sessionID)
		},
		Limits:           m.Limits.Effective,
		IsEscalated:      m.Escalated,
		WorkflowSource:   m.Workflow,
		CoordinatorFrame: m.Frame,
		BoardWillForceInject: func(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) bool {
			return m.Runtime.Board().BoardWillForceInject(ctx, sess, run)
		},
		QueueInform: func(ctx context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
			m.Guidance.Emit(ctx, sessionID, inform, env)
		},
		HasQueuedKick: func(sessionID, kickID string) bool {
			return m.Runtime.Kicks().HasQueuedKick(sessionID, kickID)
		},
		DropPendingKicksForBatchSeq: func(sessionID string, batchSeq int) {
			m.Runtime.Kicks().DropPendingKicksForBatchSeq(sessionID, batchSeq)
		},
		DropPendingKicksBeforeBatchSeq: func(sessionID string, liveSeq int) {
			m.Runtime.Kicks().DropPendingKicksBeforeBatchSeq(sessionID, liveSeq)
		},
		OnLoopQuiescent: func(ctx context.Context, sessionID string) {
			if err := m.Settlement.SettlePending(ctx, sessionID); err != nil {
				slog.ErrorContext(ctx, "settle deferred user turn", "session_id", sessionID, "error", err)
			}
			m.Admission.RoundEnd(ctx, sessionID)
		},
		IsCoordinatorSession:    m.CoordinatorSession,
		WorkflowObligationsOpen: m.Guidance.WorkflowObligationsOpen,
		WorkerCycleIdle: func(ctx context.Context, sess *api.Session, completingJobID string) (bool, error) {
			if m == nil || sess == nil {
				return true, nil
			}
			return workeroutcomes.ParentSessionWorkerCycleIdle(ctx, m.Workers, sess.ProjectID, sess.ID, completingJobID)
		},
		HostWakeOverlayPromoteDue: func(ctx context.Context, sessionID string) bool {
			if m == nil {
				return false
			}
			sess, err := m.Sessions.Get(ctx, sessionID)
			if err != nil || sess == nil {
				return false
			}
			state := m.State.ForSession(ctx, sess)
			return len(state.PendingOverlayIDs) > 0
		},
		ScanCycleOpen: func(ctx context.Context, sessionID string) bool {
			return m != nil && m.ScanInFlight != nil && m.ScanInFlight(ctx, sessionID)
		},
		ProcessRunning:   m.Processes.HandlesRunning,
		ProcessState:     m.Processes.HandleState,
		ProcessReport:    m.Processes.Report,
		PublishWaitLease: m.PublishWaitLease,
		HostWakeActionable: loopwake.BuildHostWakeActionable(loopwake.HostWakeActionableDeps{
			GetMessages: func(ctx context.Context, sessionID string) ([]api.Message, error) {
				if m == nil {
					return nil, nil
				}
				return m.Sessions.GetMessages(ctx, sessionID)
			},
			GetSession: func(ctx context.Context, sessionID string) (*api.Session, error) {
				if m == nil {
					return nil, nil
				}
				return m.Sessions.Get(ctx, sessionID)
			},
			ImplementSessionState: func(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
				if m == nil || sess == nil {
					return surface.ImplementSessionState{}
				}
				return m.State.ForSession(ctx, sess)
			},
			ActiveRun: func(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
				if m == nil || m.ActiveRuns == nil {
					return nil, nil
				}
				return m.ActiveRuns.ActiveBySession(ctx, sessionID)
			},
			WorkflowObligationsOpen: m.Guidance.WorkflowObligationsOpen,
		}),
	}
}
func (m *Loop) Escalated(sessionID string) bool {
	if m == nil || m.Grounding == nil {
		return false
	}
	return m.Grounding.IsEscalated(sessionID)
}
func (m *Loop) PublishWaitLease(ctx context.Context, sessionID string, lease loopwake.WaitLease) {
	if m == nil || m.Events == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.TrimSpace(lease.ActivityID) == "" {
		return
	}
	// Terminal events outlive the turn that armed them.
	ctx = context.WithoutCancel(ctx)
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	status := api.ActivityStatusDone
	if lease.Active {
		status = api.ActivityStatusActive
	}
	triggers := make([]string, 0, len(lease.Triggers))
	for _, trigger := range lease.Triggers {
		triggers = append(triggers, string(trigger))
	}
	m.Events.PublishActivity(ctx, strings.TrimSpace(sess.ProjectID), sessionID, api.ActivityEvent{
		ActivityID:   lease.ActivityID,
		SessionID:    sessionID,
		Kind:         api.ActivityKindAwaitingWake,
		Status:       status,
		StartedAt:    lease.StartedAt,
		WaitTriggers: triggers,
	})
}

func (m *Loop) CoordinatorSession(_ context.Context, sess *api.Session) bool {
	return surface.IsCoordinatorSession(sess)
}
