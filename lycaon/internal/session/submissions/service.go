package submissions

import (
	"context"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/draftqueue"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/recovery"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	people.OwnerSource
	Get(context.Context, string) (*api.Session, error)
	GetPromptSubmission(context.Context, string) (*store.PromptSubmission, error)
	PutPromptSubmission(context.Context, store.PromptSubmission) (*store.PromptSubmission, bool, error)
	ListQueuedUserPromptSubmissions(context.Context, string) ([]store.PromptSubmission, error)
	ClaimPromptSubmission(context.Context, string) (*store.PromptSubmission, bool, error)
	ClaimPromptSubmissions(context.Context, []string) ([]store.PromptSubmission, bool, error)
	FinishPromptSubmission(context.Context, string, string, store.PromptSubmissionStatus, string, store.PromptSubmissionFailure) error
	RecoverPromptSubmissions(context.Context) ([]string, error)
}

type Execute func(context.Context, string, promptinput.Input, *promptstate.Lock) (*promptresult.Result, error)
type ContinueUser func(context.Context, string, promptinput.Input) (api.Message, error)
type RoundComplete func(context.Context, string) bool
type HostBlocked func(context.Context, string) bool

// Service owns receipt admission, ordered dispatch, and atomic draft settlement.
type Service struct {
	store           Store
	queue           *queue.Store
	Gate            *lifecycle.State
	Recovery        *recovery.Service
	Spend           *spendguard.Service
	Drafts          *draftqueue.Service
	SubmissionState *submissionstate.Service
	promptLocks     *promptstate.MutexRegistry
	dispatch        promptstate.MutexRegistry
	admission       promptstate.MutexRegistry
	operations      promptstate.MutexRegistry
	execute         Execute
	continueUser    ContinueUser
	roundComplete   RoundComplete
	hostBlocked     HostBlocked
}

func New(st Store, q *queue.Store, gate *lifecycle.State, recovery *recovery.Service, spend *spendguard.Service, drafts *draftqueue.Service, state *submissionstate.Service, locks *promptstate.MutexRegistry, execute Execute, continuation ContinueUser, round RoundComplete, blocked HostBlocked) *Service {
	return &Service{store: st, queue: q, Gate: gate, Recovery: recovery, Spend: spend, Drafts: drafts, SubmissionState: state, promptLocks: locks, execute: execute, continueUser: continuation, roundComplete: round, hostBlocked: blocked}
}

func (m *Service) lockAdmission(sessionID string) func() {
	lock := m.admission.Acquire(sessionID)
	lock.Lock()
	return lock.Unlock
}

// DispatchLock exposes the same ordering lane to deterministic contention probes.
func (m *Service) DispatchLock(sessionID string) *promptstate.Lock {
	return m.dispatch.Acquire(sessionID)
}

// Detached starts a new asynchronous dispatch lane.
func Detached(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, promptSubmissionDispatchContextKey{}, "")
	ctx = context.WithValue(ctx, promptSubmissionDispatchLoopContextKey{}, false)
	return context.WithValue(ctx, userPromptReceiptContextKey{}, false)
}

// LockOperation serializes preparation and replay of one public operation.
func (m *Service) LockOperation(id string) func() {
	lock := m.operations.Acquire(id)
	lock.Lock()
	return lock.Unlock
}
