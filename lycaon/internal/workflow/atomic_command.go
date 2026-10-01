package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

type workflowCommandMutation struct {
	OperationID string
	Kind        string
	InputDigest string
	ProjectDir  string
	Vars        map[string]any
	Messages    []api.Message
	Posture     api.SessionPosture
	Workers     workflowWorkerMutation
	Teardown    *workflowTeardownIntent
	Rejection   *PhaseGateUnmetError
}

type workflowWorkerMutation struct {
	HoldPending   bool
	CancelRunning bool
	CancelAll     bool
	ReleaseHeld   bool
}

type workflowCommandRejection struct {
	Kind       string   `json:"kind"`
	Phase      string   `json:"phase"`
	Reason     string   `json:"reason"`
	FailedGate string   `json:"failed_gate"`
	Leaves     []string `json:"failed_leaves"`
}

type workflowStartMutation struct {
	ProjectDir                string
	Vars                      map[string]any
	Messages                  []api.Message
	Posture                   api.SessionPosture
	AllowReplacementRebase    bool
	ReplacementTeardowns      map[string]*workflowTeardownIntent
	replacementRebaseAttempts int
}

type workflowChildStartMutation struct {
	ProjectDir string
	Vars       map[string]any
	Messages   []api.Message
	Posture    api.SessionPosture
}

// Phase advancement uses separate receipts for advances and consumed markers.
const (
	advanceCommandKind        = "advance"
	advanceAlreadyCommandKind = "advance_already"
)

type workflowCommandContextKey struct{}
type workflowCommandOperationKey struct{}

type workflowCommandMeta struct {
	Kind    string
	Payload any
}

func withWorkflowCommand(ctx context.Context, kind string, payload any) context.Context {
	return context.WithValue(ctx, workflowCommandContextKey{}, workflowCommandMeta{Kind: kind, Payload: payload})
}

func withWorkflowCommandOperation(ctx context.Context, operationID string) context.Context {
	return context.WithValue(ctx, workflowCommandOperationKey{}, operationID)
}

func workflowCommandFromContext(ctx context.Context, fallback string, payload any) workflowCommandMeta {
	if meta, ok := ctx.Value(workflowCommandContextKey{}).(workflowCommandMeta); ok && meta.Kind != "" {
		return meta
	}
	return workflowCommandMeta{Kind: fallback, Payload: payload}
}

func newCommandBoundary(run *api.WorkflowRun, sourceRevision int64, event, phase, reason string) api.Message {
	msg := newBoundaryMessage(run, event, phase, reason)
	msg.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow:%s:%d:%s", run.ID, sourceRevision, event))).String()
	return msg
}

func workflowOperationMessageID(operationID, role string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("workflow-operation:"+operationID+":"+role)).String()
}

func workflowOperationRunID(operationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("workflow-operation:"+operationID+":run")).String()
}

func workflowCommandMessageID(run *api.WorkflowRun, kind, role string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-command:%s:%d:%s:%s", run.ID, run.Revision, kind, role))).String()
}

// workflowAnnouncementMessageID keys one question card to its run and phase.
// Revision is excluded: a vars stamp that re-applies at a newer
// revision must append the same logical card, not a second one.
func workflowAnnouncementMessageID(runID, phaseID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("workflow-announcement:"+runID+":"+phaseID)).String()
}

func workflowStartDigest(sessionID string, req api.StartWorkflowRunRequest) (string, error) {
	req.OperationID = ""
	raw, err := json.Marshal(struct {
		SessionID string                      `json:"session_id"`
		Request   api.StartWorkflowRunRequest `json:"request"`
	}{SessionID: sessionID, Request: req})
	if err != nil {
		return "", fmt.Errorf("encode workflow start: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func workflowCommandDigest(kind string, payload any) (string, error) {
	raw, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Payload any    `json:"payload"`
	}{Kind: kind, Payload: payload})
	if err != nil {
		return "", fmt.Errorf("encode workflow command: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (m *RunManager) replayCommand(ctx context.Context, runID, kind string, payload any) (*api.WorkflowRun, bool, error) {
	digest, err := workflowCommandDigest(kind, payload)
	if err != nil {
		return nil, false, err
	}
	if operationID, _ := ctx.Value(workflowCommandOperationKey{}).(string); operationID != "" {
		if replayed, ok, replayErr := m.Store.ReplayCommandOperation(ctx, operationID, kind, digest); replayErr != nil || ok {
			return replayed, ok, replayErr
		}
	}
	expected, _ := ctx.Value(expectedRevisionContextKey{}).(int64)
	if expected <= 0 {
		return nil, false, nil
	}
	return m.Store.ReplayCommand(ctx, runID, expected, kind, digest)
}

func (m *RunManager) replayCommandOperation(ctx context.Context, operationID, kind string, payload any) (*api.WorkflowRun, bool, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, false, nil
	}
	digest, err := workflowCommandDigest(kind, payload)
	if err != nil {
		return nil, false, err
	}
	return m.Store.ReplayCommandOperation(ctx, operationID, kind, digest)
}

// replayAdvanceToolOperation replays either phase-advance receipt.
func (m *RunManager) replayAdvanceToolOperation(ctx context.Context, operationID string) (run *api.WorkflowRun, already, ok bool, err error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, false, false, nil
	}
	digest, err := workflowCommandDigest(advanceCommandKind, struct{}{})
	if err != nil {
		return nil, false, false, err
	}
	run, ok, err = m.Store.ReplayCommandOperation(ctx, operationID, advanceCommandKind, digest)
	if ok || err == nil || !errors.Is(err, ErrRunRevisionConflict) {
		return run, false, ok, err
	}
	// Kind mismatch: the operation may hold the already-advanced receipt instead.
	alreadyDigest, digestErr := workflowCommandDigest(advanceAlreadyCommandKind, struct{}{})
	if digestErr != nil {
		return nil, false, false, digestErr
	}
	alreadyRun, alreadyOK, alreadyErr := m.Store.ReplayCommandOperation(ctx, operationID, advanceAlreadyCommandKind, alreadyDigest)
	if alreadyOK {
		return alreadyRun, true, true, alreadyErr
	}
	return nil, false, false, err
}

func workflowCommandOperationID(ctx context.Context, run *api.WorkflowRun) string {
	if operationID, _ := ctx.Value(workflowCommandOperationKey{}).(string); strings.TrimSpace(operationID) != "" {
		return strings.TrimSpace(operationID)
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-command:%s:%d", run.ID, run.Revision))).String()
}

func (m *RunManager) commitCommand(ctx context.Context, run *api.WorkflowRun, kind string, payload any, vars map[string]any, boundary *api.Message, posture api.SessionPosture, workers workflowWorkerMutation, teardown *workflowTeardownIntent) error {
	messages := make([]api.Message, 0, 1)
	if boundary != nil {
		messages = append(messages, *boundary)
	}
	return m.commitCommandMessages(ctx, run, kind, payload, vars, messages, posture, workers, teardown)
}

func (m *RunManager) commitCommandMessages(ctx context.Context, run *api.WorkflowRun, kind string, payload any, vars map[string]any, messages []api.Message, posture api.SessionPosture, workers workflowWorkerMutation, teardown *workflowTeardownIntent) error {
	digest, err := workflowCommandDigest(kind, payload)
	if err != nil {
		return err
	}
	if IsTerminal(run.Status) && vars == nil {
		vars, err = m.Store.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return err
		}
	}
	return m.Store.CommitCommand(ctx, run, workflowCommandMutation{
		OperationID: workflowCommandOperationID(ctx, run), Kind: kind, InputDigest: digest,
		ProjectDir: m.projectDirForRun(ctx, run), Vars: vars,
		Messages: messages, Posture: workflowMutationPosture(run, vars, posture), Workers: workers, Teardown: teardown,
	})
}

func (m *RunManager) commitRejectedCommand(
	ctx context.Context,
	run *api.WorkflowRun,
	kind string,
	payload any,
	vars map[string]any,
	rejection *PhaseGateUnmetError,
) error {
	digest, err := workflowCommandDigest(kind, payload)
	if err != nil {
		return err
	}
	if err := m.Store.CommitCommand(ctx, run, workflowCommandMutation{
		OperationID: workflowCommandOperationID(ctx, run), Kind: kind, InputDigest: digest,
		ProjectDir: m.projectDirForRun(ctx, run), Vars: vars, Rejection: rejection,
	}); err != nil {
		return err
	}
	return rejection
}
