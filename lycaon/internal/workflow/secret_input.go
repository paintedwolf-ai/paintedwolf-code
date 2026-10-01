package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/people"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// SecretCaptureRequest carries one protected response to storage.
type SecretCaptureRequest struct {
	ProjectID, RootSessionID, SessionID, OperationID string
	Name, Purpose, Scope, Value                      string
	// PersonID is the person who answered with the value.
	PersonID    string
	AgentUseTTL time.Duration
}

// SecretCaptureResult contains value-free storage metadata.
type SecretCaptureResult struct {
	Reference string
	Discard   func(context.Context) error
}

// SecretCapture stores one protected response.
type SecretCapture func(ctx context.Context, req SecretCaptureRequest) (SecretCaptureResult, error)

// SetSecretCapture installs protected storage for secret ask_user responses.
func (m *RunManager) SetSecretCapture(capture SecretCapture) {
	if m != nil {
		m.SecretCapture = capture
	}
}

// ResolveUserSecret stores a pending response without persisting its raw value.
func (m *RunManager) ResolveUserSecret(
	ctx context.Context,
	sessionID, runID, phaseID, value string,
) (*api.WorkflowRun, error) {
	if m == nil || m.SecretCapture == nil {
		return nil, fmt.Errorf("managed secret storage not configured")
	}
	if value == "" {
		return nil, ErrFeedbackEmptyResponse
	}
	answerer, err := people.Deciding(ctx)
	if err != nil {
		return nil, err
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.SessionID != sessionID {
		return nil, ErrRunNotFound
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	ask, ok := coordinatorAskPendingFromVars(vars)
	if !ok || ask.ID != phaseID || ask.ResponseType != workflowdef.FeedbackResponseSecret || ask.Secret == nil {
		return nil, ErrFeedbackNotPending
	}
	if err := validateCoordinatorAskForResolution(run, ask, phaseID); err != nil {
		return nil, err
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	rootSessionID := sessionID
	if m.RootSessionID != nil {
		if root := m.RootSessionID(ctx, sessionID); root != "" {
			rootSessionID = root
		}
	}
	result, err := m.SecretCapture(ctx, SecretCaptureRequest{
		ProjectID: sess.ProjectID, RootSessionID: rootSessionID, SessionID: sessionID,
		OperationID: "ask_user:" + ask.ID, Name: ask.Secret.Name, Purpose: ask.Secret.Purpose,
		Scope: ask.Secret.Scope, Value: value, PersonID: answerer.ID,
		AgentUseTTL: time.Duration(ask.Secret.AgentUseTTLSeconds) * time.Second,
	})
	if err != nil {
		if result.Discard != nil {
			return nil, errors.Join(err, result.Discard(context.WithoutCancel(ctx)))
		}
		return nil, err
	}
	if result.Reference == "" {
		if result.Discard != nil {
			return nil, errors.Join(
				fmt.Errorf("managed secret storage returned an empty reference"),
				result.Discard(context.WithoutCancel(ctx)),
			)
		}
		return nil, fmt.Errorf("managed secret storage returned an empty reference")
	}

	var answered coordinatorAsk
	stamped, err := m.StampRunVars(ctx, runID, func(_ context.Context, current *api.WorkflowRun, currentVars map[string]any) (map[string]any, bool, error) {
		pending, ok := coordinatorAskPendingFromVars(currentVars)
		if !ok || pending.ID != phaseID || pending.ResponseType != workflowdef.FeedbackResponseSecret {
			return nil, false, ErrFeedbackNotPending
		}
		if err := validateCoordinatorAskForResolution(current, pending, phaseID); err != nil {
			return nil, false, err
		}
		now := time.Now().UTC()
		pending.State = coordinatorAskAnswered
		pending.Response = result.Reference
		pending.ResolvedBy, pending.ResolvedByPersonID = "user", answerer.ID
		pending.AnsweredAt = &now
		currentVars = setFeedbackResponse(currentVars, phaseID, result.Reference)
		currentVars = setCoordinatorAsk(currentVars, pending)
		currentVars = stampHitlConsulted(currentVars, current.CurrentPhase)
		answered = pending
		return currentVars, true, nil
	})
	if err != nil {
		if result.Discard != nil {
			return nil, errors.Join(err, result.Discard(context.WithoutCancel(ctx)))
		}
		return nil, err
	}
	if stamped == nil {
		if result.Discard != nil {
			return nil, errors.Join(ErrRunNotFound, result.Discard(context.WithoutCancel(ctx)))
		}
		return nil, ErrRunNotFound
	}
	m.persistCoordinatorAskAnswer(ctx, sessionID, answered)
	m.stampFeedbackAnswer(ctx, sessionID, runID, phaseID, result.Reference, answerer.ID)
	m.notifyFeedbackResolved(ctx, sessionID, runID, phaseID, result.Reference)
	return m.TryAutoAdvance(WithExpectedRevision(ctx, stamped.Revision), runID)
}
