package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/slash"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// promptEchoMessageID is the id for a submission's user row: the submission
// id itself, so clients reconcile their pending send by exact id match.
func promptEchoMessageID(submissionID string) string {
	if id := strings.TrimSpace(submissionID); id != "" {
		return id
	}
	return uuid.NewString()
}

// TrySlashPrompt starts or exits a workflow when text matches a slash command.
// submissionID is the prompt submission's operation id; the appended user row
// echoes it as its message id.
func (m *RunManager) TrySlashPrompt(ctx context.Context, sessionID, text, submissionID string) (*promptresult.Result, bool, error) {
	if m == nil {
		return nil, false, nil
	}
	trigger, ok := slash.ParseCommand(text)
	if !ok {
		return nil, false, nil
	}
	switch trigger {
	case "/exit", "/stop":
		return m.trySlashExit(ctx, sessionID, text, submissionID)
	}
	manifest, found := m.Manifests.FindTriggerMatch(trigger)
	if !found {
		return nil, false, nil
	}
	if !manifest.Manifest.IsCatalogVisible() {
		return nil, true, workflowdef.ErrUnknownWorkflow
	}
	request := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), trigger))
	run, err := m.startHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      manifest.Manifest.ID,
		WorkflowVersion: manifest.Manifest.Version,
		PresetID:        manifest.PresetID,
		OperationID:     promptEchoMessageID(submissionID),
		Request:         request,
	}, text)
	if err != nil {
		return nil, true, err
	}
	startID := strings.TrimSpace(run.StartMessageID)
	if startID == "" {
		return &promptresult.Result{MessageID: run.ID}, true, nil
	}
	return &promptresult.Result{
		MessageID: startID,
	}, true, nil
}

func (m *RunManager) trySlashExit(ctx context.Context, sessionID, text, submissionID string) (*promptresult.Result, bool, error) {
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return nil, true, err
	}
	if active == nil {
		return nil, true, ErrNoActiveRun
	}
	reason := "user_exit"
	// Append and publish share one slice so the store's stamps (run id, seq)
	// reach the published copy.
	userMsgs := []api.Message{{
		ID:        promptEchoMessageID(submissionID),
		Role:      api.MessageRoleUser,
		Content:   strings.TrimSpace(text),
		CreatedAt: time.Now().UTC(),
	}}
	if err := m.StampAndAppendMessages(ctx, sessionID, userMsgs...); err != nil {
		return nil, true, err
	}
	m.publishMessageAppends(ctx, sessionID, userMsgs...)
	run, err := m.Exit(ctx, sessionID, active.ID, active.Revision, reason)
	if err != nil {
		return nil, true, err
	}
	endID := strings.TrimSpace(run.EndMessageID)
	if endID == "" {
		return &promptresult.Result{MessageID: run.ID}, true, nil
	}
	return &promptresult.Result{
		MessageID: endID,
	}, true, nil
}
