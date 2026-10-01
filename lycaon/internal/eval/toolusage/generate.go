package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// GenerateTask is one session a training-data run drives: the request, its
// follow-ups, the session's posture, coordinator model and workflow, and metadata the
// run echoes into its manifest without reading.
type GenerateTask struct {
	CorpusTask
	Posture    wire.SessionPosture `json:"posture,omitempty"`
	ProviderID string              `json:"provider_id,omitempty"`
	Model      string              `json:"model,omitempty"`
	// Workflow and WorkflowVersion select the exact definition receiving the prompt.
	Workflow        string          `json:"workflow,omitempty"`
	WorkflowVersion string          `json:"workflow_version,omitempty"`
	Meta            json.RawMessage `json:"meta,omitempty"`
}

// GenerateOptions configures a training-data run against one sidecar and one
// project. Every task gets its own session; a task that fails is recorded and
// aborted, and the run continues.
type GenerateOptions struct {
	BaseURL     string
	Token       string
	ProjectDir  string
	ProjectName string
	Tasks       []GenerateTask
	// Timeout bounds each prompt; a run without one could wait forever on a
	// session nothing will answer.
	Timeout    time.Duration
	Unattended UnattendedPolicy
	Approvals  []ApprovalRule
	Manifest   io.Writer
}

// ManifestEntry records one driven task.
type ManifestEntry struct {
	WorkflowID      string              `json:"workflow_id,omitempty"`
	WorkflowVersion string              `json:"workflow_version,omitempty"`
	TaskID          string              `json:"task_id"`
	RootSession     string              `json:"root_session"`
	Status          string              `json:"status"`
	Error           string              `json:"error,omitempty"`
	DurationMS      int64               `json:"duration_ms"`
	Responses       []AutomaticResponse `json:"automatic_responses,omitempty"`
	Meta            json.RawMessage     `json:"meta,omitempty"`
}

// Generation task statuses.
const (
	GenerateSettled     = "settled"
	GenerateTimeout     = "timeout"
	GenerateInteraction = "interaction_exhausted"
	GenerateFailed      = "failed"
)

// RunGenerate drives every task and writes one manifest entry per task.
func RunGenerate(ctx context.Context, opts GenerateOptions) error {
	if opts.Timeout <= 0 {
		return fmt.Errorf("a generation run requires a per-prompt timeout")
	}
	if err := opts.Unattended.validate(); err != nil {
		return err
	}
	if err := validateApprovalRules(opts.Approvals); err != nil {
		return err
	}
	if opts.Manifest == nil {
		return fmt.Errorf("a generation run requires a manifest")
	}
	for _, task := range opts.Tasks {
		if err := task.validateWorkflow(); err != nil {
			return err
		}
	}
	client := newLiveClient(strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"), opts.Token)
	proj, err := client.createProject(ctx, opts.ProjectDir, opts.ProjectName)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	enabled := map[[2]string]bool{}
	for _, task := range opts.Tasks {
		identity := [2]string{task.Workflow, task.WorkflowVersion}
		if task.Workflow == "" || enabled[identity] {
			continue
		}
		if err := client.enableWorkflowFixture(ctx, proj.ID, task.Workflow, task.WorkflowVersion); err != nil {
			return fmt.Errorf("enable workflow %s: %w", task.Workflow, err)
		}
		enabled[identity] = true
	}
	enc := json.NewEncoder(opts.Manifest)
	for _, task := range opts.Tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry, err := client.generateOne(ctx, proj.ID, task, opts)
		if err != nil {
			return fmt.Errorf("task %q: %w", task.ID, err)
		}
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}
	return nil
}

func (c *liveClient) generateOne(ctx context.Context, projectID string, task GenerateTask, opts GenerateOptions) (ManifestEntry, error) {
	started := time.Now()
	sess, err := c.createSession(ctx, wire.CreateSessionRequest{ProjectID: projectID, Posture: task.Posture, ProviderID: task.ProviderID, Model: task.Model})
	if err != nil {
		return ManifestEntry{}, fmt.Errorf("create session: %w", err)
	}
	result := CaseReport{ID: task.ID, SessionID: sess.ID}
	c.onHumanInput = c.unattendedObserver(ctx, &result, opts.Unattended, opts.Approvals, func(CaseReport) error { return nil })
	defer func() { c.onHumanInput = nil }()
	entry := ManifestEntry{WorkflowID: task.Workflow, WorkflowVersion: task.WorkflowVersion, TaskID: task.ID, RootSession: sess.ID, Status: GenerateSettled, Meta: task.Meta}
	var runErr error
	if task.Workflow == "" {
		runErr = c.runScenario(ctx, sess.ID, task.CorpusTask, opts.Timeout)
	} else {
		runErr = c.runWorkflowTask(ctx, sess.ID, task, opts.Timeout)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return ManifestEntry{}, ctx.Err()
		}
		entry.Status, entry.Error = generateStatus(runErr), runErr.Error()
		if abortErr := c.abortScenario(ctx, sess.ID); abortErr != nil {
			return ManifestEntry{}, fmt.Errorf("%w; abort failed: %w", runErr, abortErr)
		}
	}
	entry.DurationMS = time.Since(started).Milliseconds()
	entry.Responses = result.AutomaticResponses
	return entry, nil
}

// runWorkflowTask starts the task's workflow with the prompt as its request, the way a
// person starts one from the composer, waits for that turn, then sends the follow-ups.
func (c *liveClient) runWorkflowTask(ctx context.Context, sessionID string, task GenerateTask, timeout time.Duration) error {
	if err := c.startWorkflowRequest(ctx, sessionID, task, timeout); err != nil {
		return err
	}
	for i, followUp := range task.FollowUps {
		if err := c.postPromptAndWait(ctx, sessionID, followUp.Prompt, timeout); err != nil {
			return fmt.Errorf("follow-up %d: %w", i+1, err)
		}
	}
	return nil
}

func (c *liveClient) startWorkflowRequest(ctx context.Context, sessionID string, task GenerateTask, timeout time.Duration) error {
	ctx, cancel := taskContext(ctx, timeout)
	defer cancel()
	if err := c.waitSessionPrepared(ctx, sessionID); err != nil {
		return err
	}
	body, err := json.Marshal(wire.StartWorkflowRunRequest{OperationID: uuid.NewString(), WorkflowID: task.Workflow, WorkflowVersion: task.WorkflowVersion, Request: task.Prompt})
	if err != nil {
		return err
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/v1/sessions/"+sessionID+"/workflow-runs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	run, err := decodeJSON[wire.WorkflowRun](c.do(request)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return fmt.Errorf("start workflow %s: %w", task.Workflow, err)
	}
	if run.SessionID != sessionID || run.WorkflowID != task.Workflow || run.WorkflowVersion != task.WorkflowVersion {
		return fmt.Errorf("workflow %s@%s started as %s@%s on session %s", task.Workflow, task.WorkflowVersion, run.WorkflowID, run.WorkflowVersion, run.SessionID)
	}
	return c.waitScenarioCompletion(ctx, sessionID, func(ctx context.Context) (bool, error) {
		return c.requestTurnSettled(ctx, sessionID)
	})
}

// requestTurnSettled reports whether the session is idle after answering the request that
// started its workflow: a user message followed by a final answer.
func (c *liveClient) requestTurnSettled(ctx context.Context, sessionID string) (bool, error) {
	pre, err := c.getSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if pre.Status == wire.SessionStatusError {
		return false, fmt.Errorf("session %s entered error state", sessionID)
	}
	messages, err := c.listMessages(ctx, sessionID)
	if err != nil {
		return false, err
	}
	post, err := c.getSession(ctx, sessionID)
	if err != nil || pre.Status != wire.SessionStatusIdle || post.Status != wire.SessionStatusIdle {
		return false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == wire.MessageRoleUser {
			return finalAnswer(messages[i+1:]) != "", nil
		}
	}
	return false, nil
}

func generateStatus(err error) string {
	var exhausted interactionExhausted
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return GenerateTimeout
	case errors.As(err, &exhausted):
		return GenerateInteraction
	default:
		return GenerateFailed
	}
}
