package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptHostTurn runs a host-initiated turn behind a durable receipt.
func (m *Manager) PromptHostTurn(ctx context.Context, sessionID string, origin store.PromptSubmissionOrigin, text string) (*promptresult.Result, error) {
	in := PromptInput{Text: text, HostSignal: hostSignalForOrigin(origin), WorkerJobID: workercontext.Job(ctx)}
	if origin == store.PromptSubmissionOriginWorkerCloseout {
		in.ProseFinish = true
	}
	return m.runHostTurn(ctx, sessionID, origin, in)
}

func hostSignalForOrigin(origin store.PromptSubmissionOrigin) *PromptHostSignal {
	switch origin {
	case store.PromptSubmissionOriginWorkerCloseout:
		return &PromptHostSignal{Kind: api.MessageKindHostKick, ID: string(anchor.WorkerCloseout)}
	case store.PromptSubmissionOriginGroundingRetry:
		return &PromptHostSignal{Kind: api.MessageKindHostKick, ID: string(anchor.WorkerCitationGrounding)}
	default:
		return nil
	}
}

// runHostTurn admits and executes one host receipt.
func (m *Manager) runHostTurn(
	ctx context.Context,
	sessionID string,
	origin store.PromptSubmissionOrigin,
	in PromptInput,
) (*promptresult.Result, error) {
	return m.runHostTurnWithAdmission(ctx, sessionID, origin, in, nil)
}

func (m *Manager) runHostTurnWithAdmission(
	ctx context.Context,
	sessionID string,
	origin store.PromptSubmissionOrigin,
	in PromptInput,
	onAdmitted func() error,
) (*promptresult.Result, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("prompt manager unavailable")
	}
	ctx, finishWork, err := m.engineWork.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finishWork()

	if !origin.HostInitiated() {
		return nil, fmt.Errorf("host turn requires a host origin, got %q", origin)
	}
	ctx, unlockDispatch := m.lockPromptSubmissionDispatch(ctx, sessionID)
	defer unlockDispatch()
	if origin == store.PromptSubmissionOriginLoopWake && m.hostTurnBlocked(ctx, sessionID) {
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
	lock := m.promptState.Prompt.Acquire(sessionID)
	lock.Lock()
	return m.claimAndRunSubmissionLocked(ctx, row.ID, lock)
}
