package submissions

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptHostTurn runs a host-initiated turn behind a durable receipt.
func (m *Service) PromptHostTurn(ctx context.Context, sessionID string, origin store.PromptSubmissionOrigin, text string) (*promptresult.Result, error) {
	in := promptinput.Input{Text: text, HostSignal: hostSignalForOrigin(origin), WorkerJobID: workercontext.Job(ctx)}
	if origin == store.PromptSubmissionOriginWorkerCloseout {
		in.ProseFinish = true
	}
	return m.runHostTurn(ctx, sessionID, origin, in)
}

func hostSignalForOrigin(origin store.PromptSubmissionOrigin) *promptinput.HostSignal {
	switch origin {
	case store.PromptSubmissionOriginWorkerCloseout:
		return &promptinput.HostSignal{Kind: api.MessageKindHostKick, ID: string(anchor.WorkerCloseout)}
	case store.PromptSubmissionOriginGroundingRetry:
		return &promptinput.HostSignal{Kind: api.MessageKindHostKick, ID: string(anchor.WorkerCitationGrounding)}
	default:
		return nil
	}
}

// runHostTurn admits and executes one host receipt.
func (m *Service) runHostTurn(
	ctx context.Context,
	sessionID string,
	origin store.PromptSubmissionOrigin,
	in promptinput.Input,
) (*promptresult.Result, error) {
	return m.HostTurnWithAdmission(ctx, sessionID, origin, in, nil)
}

func (m *Service) HostTurnWithAdmission(
	ctx context.Context,
	sessionID string,
	origin store.PromptSubmissionOrigin,
	in promptinput.Input,
	onAdmitted func() error,
) (*promptresult.Result, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("prompt manager unavailable")
	}
	if !origin.HostInitiated() {
		return nil, fmt.Errorf("host turn requires a host origin, got %q", origin)
	}
	ctx, unlockDispatch := m.LockDispatch(ctx, sessionID)
	defer unlockDispatch()
	if origin == store.PromptSubmissionOriginLoopWake && m.hostBlocked(ctx, sessionID) {
		return nil, nil
	}
	operationID := strings.TrimSpace(in.SubmissionID)
	if operationID == "" {
		operationID = uuid.NewString()
	}
	row, _, err := m.admitPrompt(ctx, sessionID, operationID, origin, in, in)
	if err != nil {
		return nil, err
	}
	if onAdmitted != nil {
		if err := onAdmitted(); err != nil {
			return nil, err
		}
	}
	lock := m.promptLocks.Acquire(sessionID)
	lock.Lock()
	return m.RunClaimedLocked(ctx, row.ID, lock)
}
