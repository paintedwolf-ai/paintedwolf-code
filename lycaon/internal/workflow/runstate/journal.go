package runstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"strings"
	"time"

	"github.com/google/uuid"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type CommandReceipts interface {
	ReplayCommand(context.Context, string, int64, string, string) (*api.WorkflowRun, bool, error)
	ReplayCommandOperation(context.Context, string, string, string) (*api.WorkflowRun, bool, error)
	CommitCommand(context.Context, *api.WorkflowRun, CommandMutation) error
}

type ScaffoldReader interface {
	GetScaffoldVars(context.Context, string) (map[string]any, error)
}
type ProjectDirectories interface {
	ProjectDirForRun(context.Context, *api.WorkflowRun) string
}

// Journal admits commands through their durable replay and commit receipts.
type Journal struct {
	Commands    CommandReceipts
	Vars        ScaffoldReader
	Directories ProjectDirectories
}

// Phase advancement uses separate receipts for advances and consumed markers.
const (
	AdvanceCommandKind        = "advance"
	AdvanceAlreadyCommandKind = "advance_already"
)

type workflowCommandContextKey struct{}
type workflowCommandOperationKey struct{}

type CommandMeta struct {
	Kind    string
	Payload any
}

func WithCommand(ctx context.Context, kind string, payload any) context.Context {
	return context.WithValue(ctx, workflowCommandContextKey{}, CommandMeta{Kind: kind, Payload: payload})
}

func WithCommandOperation(ctx context.Context, operationID string) context.Context {
	return context.WithValue(ctx, workflowCommandOperationKey{}, operationID)
}

func CommandFromContext(ctx context.Context, fallback string, payload any) CommandMeta {
	if meta, ok := ctx.Value(workflowCommandContextKey{}).(CommandMeta); ok && meta.Kind != "" {
		return meta
	}
	return CommandMeta{Kind: fallback, Payload: payload}
}

func NewCommandBoundary(run *api.WorkflowRun, sourceRevision int64, event, phase, reason string) api.Message {
	msg := NewBoundaryMessage(run, event, phase, reason)
	msg.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow:%s:%d:%s", run.ID, sourceRevision, event))).String()
	return msg
}

func OperationMessageID(operationID, role string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("workflow-operation:"+operationID+":"+role)).String()
}

func OperationRunID(operationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("workflow-operation:"+operationID+":run")).String()
}

func CommandMessageID(run *api.WorkflowRun, kind, role string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-command:%s:%d:%s:%s", run.ID, run.Revision, kind, role))).String()
}

// AnnouncementMessageID keys one question card to its run and phase.
// Revision is excluded: a vars stamp that re-applies at a newer
// revision must append the same logical card, not a second one.
func AnnouncementMessageID(runID, phaseID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("workflow-announcement:"+runID+":"+phaseID)).String()
}

func StartDigest(sessionID string, req api.StartWorkflowRunRequest) (string, error) {
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

func CommandDigest(kind string, payload any) (string, error) {
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

func (m *Journal) Replay(ctx context.Context, runID, kind string, payload any) (*api.WorkflowRun, bool, error) {
	digest, err := CommandDigest(kind, payload)
	if err != nil {
		return nil, false, err
	}
	if operationID, _ := ctx.Value(workflowCommandOperationKey{}).(string); operationID != "" {
		if replayed, ok, replayErr := m.Commands.ReplayCommandOperation(ctx, operationID, kind, digest); replayErr != nil || ok {
			return replayed, ok, replayErr
		}
	}
	expected, _ := ctx.Value(expectedRevisionContextKey{}).(int64)
	if expected <= 0 {
		return nil, false, nil
	}
	return m.Commands.ReplayCommand(ctx, runID, expected, kind, digest)
}

func (m *Journal) ReplayOperation(ctx context.Context, operationID, kind string, payload any) (*api.WorkflowRun, bool, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, false, nil
	}
	digest, err := CommandDigest(kind, payload)
	if err != nil {
		return nil, false, err
	}
	return m.Commands.ReplayCommandOperation(ctx, operationID, kind, digest)
}

// ReplayAdvanceOperation replays either phase-advance receipt.
func (m *Journal) ReplayAdvanceOperation(ctx context.Context, operationID string) (run *api.WorkflowRun, already, ok bool, err error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, false, false, nil
	}
	digest, err := CommandDigest(AdvanceCommandKind, struct{}{})
	if err != nil {
		return nil, false, false, err
	}
	run, ok, err = m.Commands.ReplayCommandOperation(ctx, operationID, AdvanceCommandKind, digest)
	if ok || err == nil || !errors.Is(err, ErrRevisionConflict) {
		return run, false, ok, err
	}
	// Kind mismatch: the operation may hold the already-advanced receipt instead.
	alreadyDigest, digestErr := CommandDigest(AdvanceAlreadyCommandKind, struct{}{})
	if digestErr != nil {
		return nil, false, false, digestErr
	}
	alreadyRun, alreadyOK, alreadyErr := m.Commands.ReplayCommandOperation(ctx, operationID, AdvanceAlreadyCommandKind, alreadyDigest)
	if alreadyOK {
		return alreadyRun, true, true, alreadyErr
	}
	return nil, false, false, err
}

func CommandOperationID(ctx context.Context, run *api.WorkflowRun) string {
	if operationID, _ := ctx.Value(workflowCommandOperationKey{}).(string); strings.TrimSpace(operationID) != "" {
		return strings.TrimSpace(operationID)
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-command:%s:%d", run.ID, run.Revision))).String()
}

func (m *Journal) Commit(ctx context.Context, run *api.WorkflowRun, kind string, payload any, vars map[string]any, boundary *api.Message, posture api.SessionPosture, workers WorkerMutation, teardown *TeardownIntent) error {
	messages := make([]api.Message, 0, 1)
	if boundary != nil {
		messages = append(messages, *boundary)
	}
	return m.CommitMessages(ctx, run, kind, payload, vars, messages, posture, workers, teardown)
}

func (m *Journal) CommitMessages(ctx context.Context, run *api.WorkflowRun, kind string, payload any, vars map[string]any, messages []api.Message, posture api.SessionPosture, workers WorkerMutation, teardown *TeardownIntent) error {
	digest, err := CommandDigest(kind, payload)
	if err != nil {
		return err
	}
	if IsTerminal(run.Status) && vars == nil {
		vars, err = m.Vars.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return err
		}
	}
	return m.Commands.CommitCommand(ctx, run, CommandMutation{
		OperationID: CommandOperationID(ctx, run), Kind: kind, InputDigest: digest,
		ProjectDir: m.Directories.ProjectDirForRun(ctx, run), Vars: vars,
		Messages: messages, Posture: MutationPosture(run, vars, posture), Workers: workers, Teardown: teardown,
	})
}

func (m *Journal) CommitRejected(
	ctx context.Context,
	run *api.WorkflowRun,
	kind string,
	payload any,
	vars map[string]any,
	rejection *PhaseGateUnmetError,
) error {
	digest, err := CommandDigest(kind, payload)
	if err != nil {
		return err
	}
	if err := m.Commands.CommitCommand(ctx, run, CommandMutation{
		OperationID: CommandOperationID(ctx, run), Kind: kind, InputDigest: digest,
		ProjectDir: m.Directories.ProjectDirForRun(ctx, run), Vars: vars, Rejection: rejection,
	}); err != nil {
		return err
	}
	return rejection
}

// IsTerminal reports whether a run has reached an absorbing status.
func IsTerminal(status api.WorkflowRunStatus) bool {
	switch status {
	case api.WorkflowRunStatusComplete, api.WorkflowRunStatusFailed, api.WorkflowRunStatusCanceled, api.WorkflowRunStatusInterrupted:
		return true
	default:
		return false
	}
}

type expectedRevisionContextKey struct{}

type approvalChannelContextKey struct{}

// Approval channels name how a person's blueprint approval arrived.
const (
	ApprovalChannelAPI  = "api"
	ApprovalChannelChat = "chat"
)

// WithExpectedRevision binds a human-reviewed run revision to one command.
func WithExpectedRevision(ctx context.Context, revision int64) context.Context {
	return context.WithValue(ctx, expectedRevisionContextKey{}, revision)
}

// WithApprovalChannel records how the approval being committed arrived.
func WithApprovalChannel(ctx context.Context, channel string) context.Context {
	return context.WithValue(ctx, approvalChannelContextKey{}, strings.TrimSpace(channel))
}

func WithoutExpectedRevision(ctx context.Context) context.Context {
	return context.WithValue(ctx, expectedRevisionContextKey{}, int64(0))
}

func VerifyExpectedRevision(ctx context.Context, run *api.WorkflowRun) error {
	expected, _ := ctx.Value(expectedRevisionContextKey{}).(int64)
	if expected <= 0 || run == nil || run.Revision == expected {
		return nil
	}
	return fmt.Errorf("%w: run %s expected revision %d, actual %d", ErrRevisionConflict, run.ID, expected, run.Revision)
}

func ApprovalChannel(ctx context.Context) string {
	value, _ := ctx.Value(approvalChannelContextKey{}).(string)
	return value
}

const BaselinePostureKey = "pre_workflow_posture"

func BoundaryVisibility(run *api.WorkflowRun) api.MessageVisibility {
	if run != nil && strings.TrimSpace(run.AttachPolicy) == string(workflowdef.AttachPolicySessionCreate) {
		return api.MessageVisibilityInternal
	}
	return api.MessageVisibilityTranscript
}

func NewBoundaryMessage(run *api.WorkflowRun, event, phase, reason string) api.Message {
	workflowID, workflowVersion, runID := "", "", ""
	if run != nil {
		workflowID = run.WorkflowID
		workflowVersion = run.WorkflowVersion
		runID = run.ID
	}
	meta := api.WorkflowBoundaryMeta{
		Event:           event,
		WorkflowID:      workflowID,
		WorkflowVersion: workflowVersion,
		Phase:           phase,
		Reason:          reason,
	}
	msg := api.Message{
		ID:               uuid.NewString(),
		Role:             api.MessageRoleSystem,
		Kind:             api.MessageKindWorkflowBoundary,
		Visibility:       BoundaryVisibility(run),
		WorkflowBoundary: &meta,
		CreatedAt:        time.Now().UTC(),
	}
	if runID != "" {
		msg.WorkflowRunID = runID
	}
	return msg
}

func StartBoundaryMessages(run *api.WorkflowRun, slashText, operationID string) ([]api.Message, api.Message) {
	now := time.Now().UTC()
	msgs := make([]api.Message, 0, 2)
	if text := strings.TrimSpace(slashText); text != "" {
		// The slash row echoes the operation id — the submission id clients
		// reconcile pending sends against.
		msgs = append(msgs, api.Message{
			ID: operationID, Role: api.MessageRoleUser,
			Content: text, WorkflowRunID: run.ID, CreatedAt: now,
		})
	}
	start := NewBoundaryMessage(run, "started", run.CurrentPhase, "")
	start.ID = OperationMessageID(operationID, "started")
	start.CreatedAt = now
	msgs = append(msgs, start)
	return msgs, start
}

func MutationPosture(run *api.WorkflowRun, vars map[string]any, active api.SessionPosture) api.SessionPosture {
	if run != nil && IsTerminal(run.Status) {
		baseline, _ := vars[BaselinePostureKey].(string)
		if sessionposture.ValidSessionPosture(baseline) {
			return api.SessionPosture(baseline)
		}
	}
	return active
}
