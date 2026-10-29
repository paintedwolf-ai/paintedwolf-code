package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestPlanImplementLegLoopWakesCoordinator(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	mgr := h.Sessions.Manager
	blueprintMgr := h.Workflows.Blueprints
	ctx := context.Background()
	sess := createSessionHTTP(t, srv, t.TempDir())

	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal run", err)
	}
	run = advancePlanRunToExecuteHTTP(t, h, srv, blueprintMgr, sess, run)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}

	initialAssistants := 0
	waitDeadline := func() (time.Time, bool) {
		deadline, ok := t.Deadline()
		if !ok {
			return time.Time{}, false
		}
		return time.Now().Add(time.Until(deadline) / 8), true
	}
	diagnosticCtx := t.Context()
	if limit, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		diagnosticCtx, cancel = context.WithDeadline(diagnosticCtx, limit)
		defer cancel()
	}
	for _, msg := range listMessagesHTTP(t, srv, sess.ID) {
		if msg.Role == wire.MessageRoleAssistant {
			initialAssistants++
		}
	}
	accepted := acceptPromptHTTP(t, srv, sess.ID, "plan implement leg")
	deadline, hasDeadline := waitDeadline()
	for {
		msgs := listMessagesHTTP(t, srv, sess.ID)
		seenReceipt, assistants := false, 0
		for _, msg := range msgs {
			seenReceipt = seenReceipt || msg.ID == accepted.OperationID && msg.Role == wire.MessageRoleUser
			if msg.Role == wire.MessageRoleAssistant {
				assistants++
			}
		}
		if seenReceipt && assistants > initialAssistants && getSessionHTTP(t, srv, sess.ID).Status == wire.SessionStatusIdle {
			break
		}
		if t.Context().Err() != nil || hasDeadline && time.Now().After(deadline) {
			_, reason, admissionErr := mgr.Coordinator.Runtime.CoordinatorLoop().Admission.ShouldLoopWake(diagnosticCtx, sess.ID, anchor.LegFinished)
			facts := collectCoordinatorWakeFacts(diagnosticCtx, h, sess.ID, accepted.OperationID, reason, admissionErr)
			t.Fatalf("prompt did not complete: receipt=%t assistants=%d initial=%d facts=%s", seenReceipt, assistants, initialAssistants, facts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	child, err := mgr.Workers.SpawnChild(ctx, sess.ID, wire.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "spawn child", err)
	if _, err := mgr.Workers.Summaries.Append(ctx, sess.ID, workeroutcomes.SummaryInput{
		Summary:        "implemented feature X",
		DelegationID:   "dep-e2e",
		LegID:          "leg-e2e",
		JobID:          "job-e2e",
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Status:         "complete",
	}); err != nil {
		testutil.FailErr(t, "append worker summary", err)
	}
	var allowed bool
	var reason string
	deadline, hasDeadline = waitDeadline()
	for {
		var err error
		allowed, reason, err = mgr.Coordinator.Runtime.CoordinatorLoop().Admission.ShouldLoopWake(ctx, sess.ID, anchor.LegFinished)
		if err == nil && allowed {
			break
		}
		if err != nil || reason != "session_busy" || t.Context().Err() != nil || hasDeadline && time.Now().After(deadline) {
			facts := collectCoordinatorWakeFacts(diagnosticCtx, h, sess.ID, accepted.OperationID, reason, err)
			t.Fatalf("completed leg did not become eligible to wake coordinator: reason=%q error=%v facts=%s", reason, err, facts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !allowed {
		t.Fatalf("completed leg cannot wake coordinator: %s", reason)
	}
	mgr.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-e2e", anchor.Envelope{})

	testutil.WaitFor(t, func() bool {
		mgr.Runner.Coordinator.CoordinatorLoop().Nudges.DrainPending(ctx, sess.ID)
		msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
		if err != nil {
			return false
		}
		assistants := 0
		hasAuto := false
		for _, msg := range msgs {
			if msg.Role == wire.MessageRoleAssistant {
				assistants++
			}
			if msg.Kind == wire.MessageKindHostLoopWake {
				hasAuto = true
			}
		}
		return assistants >= 2 && hasAuto
	})

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/messages", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET messages status = %d", w.Code)
	}
}

type coordinatorWakeFacts struct {
	Reason                string                        `json:"reason"`
	AdmissionError        string                        `json:"admission_error"`
	SessionStatus         *wire.SessionStatus           `json:"session_status"`
	SubmissionID          string                        `json:"submission_id"`
	SubmissionStatus      *store.PromptSubmissionStatus `json:"submission_status"`
	SubmissionErrorCode   string                        `json:"submission_error_code"`
	TurnID                string                        `json:"turn_id"`
	TurnStatus            *store.TurnStatus             `json:"turn_status"`
	RunID                 string                        `json:"run_id"`
	RunStatus             *wire.WorkflowRunStatus       `json:"run_status"`
	RunPhase              string                        `json:"run_phase"`
	Runs                  []coordinatorWakeRun          `json:"runs"`
	HumanApprovalAwaiting *bool                         `json:"human_approval_awaiting"`
	HostObligationHeld    *bool                         `json:"host_obligation_held"`
	PendingUserInput      *bool                         `json:"pending_user_input"`
	Escalated             bool                          `json:"escalated"`
	TranscriptRoles       []wire.MessageRole            `json:"transcript_roles"`
	Messages              []coordinatorWakeMessage      `json:"messages"`
	ReadErrors            map[string]string             `json:"read_errors"`
}

// coordinatorWakeRun is one workflow run of the session, active or retired,
// so a no_active_run denial names which run ended and in which phase.
type coordinatorWakeRun struct {
	ID           string                 `json:"id"`
	WorkflowID   string                 `json:"workflow_id"`
	Status       wire.WorkflowRunStatus `json:"status"`
	CurrentPhase string                 `json:"current_phase"`
	ParentRunID  string                 `json:"parent_run_id,omitempty"`
	CompletedAt  string                 `json:"completed_at,omitempty"`
}

// coordinatorWakeMessage is one transcript row's structure without its content.
type coordinatorWakeMessage struct {
	Role         wire.MessageRole `json:"role"`
	Kind         wire.MessageKind `json:"kind,omitempty"`
	HostSignalID string           `json:"host_signal_id,omitempty"`
	ToolCalls    []string         `json:"tool_calls,omitempty"`
	ResultTool   string           `json:"result_tool,omitempty"`
}

func (facts coordinatorWakeFacts) String() string {
	data, _ := json.Marshal(facts)
	return string(data)
}

func collectCoordinatorWakeFacts(ctx context.Context, h *wiring.Harness, sessionID, submissionID, reason string, admissionErr error) coordinatorWakeFacts {
	facts := coordinatorWakeFacts{Reason: reason, SubmissionID: submissionID, ReadErrors: map[string]string{}}
	readOK := func(source string, err error) bool {
		if err != nil {
			facts.ReadErrors[source] = err.Error()
		}
		return err == nil
	}
	if admissionErr != nil {
		facts.AdmissionError = admissionErr.Error()
	}
	sess, err := h.Store.Get(ctx, sessionID)
	if readOK("session", err) && sess != nil {
		facts.SessionStatus = &sess.Status
	}
	submission, err := h.Store.GetPromptSubmission(ctx, submissionID)
	if readOK("submission", err) && submission != nil {
		facts.SubmissionStatus = &submission.Status
		facts.SubmissionErrorCode = submission.ErrorCode
	}
	var status store.TurnStatus
	err = h.DB.QueryRowContext(ctx, `SELECT head.turn_id, turn.status FROM session_turn_heads head
		JOIN turns turn ON turn.id = head.turn_id WHERE head.session_id = ?`, sessionID).Scan(&facts.TurnID, &status)
	if readOK("turn", err) {
		facts.TurnStatus = &status
	}
	run, vars, err := h.Workflows.Store.Runs.ActiveStateBySession(ctx, sessionID)
	if readOK("workflow", err) {
		pending := scaffoldvars.HasPendingUserInput(vars)
		facts.PendingUserInput = &pending
		if run != nil {
			facts.RunID, facts.RunStatus, facts.RunPhase = run.ID, &run.Status, run.CurrentPhase
		}
	}
	runs, err := readCoordinatorWakeRuns(ctx, h.DB, sessionID)
	if readOK("runs", err) {
		facts.Runs = runs
	}
	awaiting, err := h.Workflows.Manager.Policy.HumanApprovalAwaiting(ctx, sessionID)
	if readOK("human_approval", err) {
		facts.HumanApprovalAwaiting = &awaiting
	}
	held, err := h.Workflows.Manager.Obligations.HostObligationHeld(ctx, sessionID)
	if readOK("host_obligation", err) {
		facts.HostObligationHeld = &held
	}
	facts.Escalated = h.Sessions.Manager.Coordinator.Loop.Escalated(sessionID)
	messages, err := h.Store.GetMessages(ctx, sessionID)
	if readOK("transcript", err) {
		facts.TranscriptRoles = make([]wire.MessageRole, 0, len(messages))
		facts.Messages = make([]coordinatorWakeMessage, 0, len(messages))
		for _, msg := range messages {
			facts.TranscriptRoles = append(facts.TranscriptRoles, msg.Role)
			entry := coordinatorWakeMessage{Role: msg.Role, Kind: msg.Kind, HostSignalID: msg.HostSignalID}
			for _, call := range msg.ToolCalls {
				entry.ToolCalls = append(entry.ToolCalls, call.Name)
			}
			if msg.ToolResult != nil {
				entry.ResultTool = msg.ToolResult.Tool
			}
			facts.Messages = append(facts.Messages, entry)
		}
	}
	return facts
}

func readCoordinatorWakeRuns(ctx context.Context, handle db.ReadHandle, sessionID string) ([]coordinatorWakeRun, error) {
	rows, err := handle.QueryContext(ctx, `SELECT id, workflow_id, status, current_phase, parent_run_id, completed_at
		FROM workflow_runs WHERE session_id = ? ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []coordinatorWakeRun{}
	for rows.Next() {
		var entry coordinatorWakeRun
		var parent, completed sql.NullString
		if err := rows.Scan(&entry.ID, &entry.WorkflowID, &entry.Status, &entry.CurrentPhase, &parent, &completed); err != nil {
			return nil, err
		}
		entry.ParentRunID, entry.CompletedAt = parent.String, completed.String
		runs = append(runs, entry)
	}
	return runs, rows.Err()
}

func TestCoordinatorWakeFacts(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	ctx := t.Context()
	project := createProjectHTTP(t, h.Server, t.TempDir())
	sess, err := h.Store.Create(h.OwnerCtx(t, ctx), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, project.ID)
	testutil.FailErr(t, "create diagnostic session", err)
	receipt, _, err := h.Store.PutPromptSubmission(ctx, store.PromptSubmission{
		ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID,
		Origin: store.PromptSubmissionOriginLoopWake, InputJSON: `{}`, InputDigest: "diagnostic",
	})
	testutil.FailErr(t, "create receipt", err)
	receipt, claimed, err := h.Store.ClaimPromptSubmission(ctx, receipt.ID)
	testutil.FailErr(t, "claim receipt", err)
	if !claimed {
		t.Fatal("receipt was not claimed")
	}
	testutil.FailErr(t, "fail receipt", h.Store.FinishPromptSubmission(ctx, receipt.ID, receipt.ClaimToken,
		store.PromptSubmissionFailed, "", store.PromptSubmissionFailure{Code: "fixture_failed", Message: "private diagnostic input"}))
	execution, err := h.Store.BeginTurn(ctx, store.TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: `{}`,
	})
	testutil.FailErr(t, "begin turn", err)
	_, err = h.Store.FinishTurn(ctx, execution.Turn.ID, execution.Attempt.ID, store.TurnStatusInterrupted, "", "", "private diagnostic input")
	testutil.FailErr(t, "interrupt turn", err)
	run := &wire.WorkflowRun{ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID,
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Status: wire.WorkflowRunStatusRunning, CurrentPhase: "research"}
	vars := map[string]any{"user_feedback": map[string]any{"research": map[string]any{"pending": true}}, "private": "private diagnostic input"}
	testutil.FailErr(t, "create workflow", h.Workflows.Store.State.CreateState(ctx, run, sess.WorkspacePath, vars))
	groundingState := grounding.NewStateStore()
	groundingState.Set(sess.ID, grounding.UngroundedCounter{Escalated: true})
	h.Sessions.Manager.Coordinator.Loop.Grounding = groundingState
	toolCallID := uuid.NewString()
	transcript := []wire.Message{
		{ID: uuid.NewString(), Role: wire.MessageRoleUser, Content: "private diagnostic input"},
		{ID: uuid.NewString(), Role: wire.MessageRoleAssistant, Content: "private diagnostic input"},
		{ID: uuid.NewString(), Role: wire.MessageRoleUser, Kind: wire.MessageKindHostLoopWake, HostSignalID: "signal-1", Content: "private diagnostic input"},
		{ID: uuid.NewString(), Role: wire.MessageRoleAssistant, Content: "private diagnostic input",
			ToolCalls: []wire.ToolCall{{ID: toolCallID, Name: "read", Args: map[string]any{"path": "private diagnostic input"}}}},
		{ID: uuid.NewString(), Role: wire.MessageRoleTool, Content: "private diagnostic input",
			ToolResult: &wire.ToolResult{Tool: "read", ToolCallID: toolCallID, Content: "private diagnostic input"}},
	}
	roles := make([]wire.MessageRole, 0, len(transcript))
	for _, msg := range transcript {
		roles = append(roles, msg.Role)
		testutil.FailErr(t, "append transcript", h.Store.AppendMessages(ctx, sess.ID, msg))
	}
	expectedRuns := []coordinatorWakeRun{{ID: run.ID, WorkflowID: "plan", Status: wire.WorkflowRunStatusRunning, CurrentPhase: "research"}}
	expectedMessages := []coordinatorWakeMessage{
		{Role: wire.MessageRoleUser}, {Role: wire.MessageRoleAssistant},
		{Role: wire.MessageRoleUser, Kind: wire.MessageKindHostLoopWake, HostSignalID: "signal-1"},
		{Role: wire.MessageRoleAssistant, ToolCalls: []string{"read"}},
		{Role: wire.MessageRoleTool, ResultTool: "read"},
	}
	facts := collectCoordinatorWakeFacts(ctx, h, sess.ID, receipt.ID, "execution_failed", nil)
	if facts.Reason != "execution_failed" || facts.SessionStatus == nil || *facts.SessionStatus != wire.SessionStatusIdle ||
		facts.SubmissionID != receipt.ID || facts.TurnID != execution.Turn.ID || facts.TurnStatus == nil || *facts.TurnStatus != store.TurnStatusInterrupted ||
		facts.SubmissionStatus == nil || *facts.SubmissionStatus != store.PromptSubmissionFailed || facts.SubmissionErrorCode != "fixture_failed" ||
		facts.RunID != run.ID || facts.RunStatus == nil || *facts.RunStatus != wire.WorkflowRunStatusRunning || facts.RunPhase != "research" ||
		facts.PendingUserInput == nil || !*facts.PendingUserInput || !facts.Escalated || !reflect.DeepEqual(facts.TranscriptRoles, roles) {
		t.Fatalf("facts = %s", facts)
	}
	if facts.HumanApprovalAwaiting == nil || *facts.HumanApprovalAwaiting || facts.HostObligationHeld == nil || *facts.HostObligationHeld || len(facts.ReadErrors) != 0 {
		t.Fatalf("holds or reads = %s", facts)
	}
	if !reflect.DeepEqual(facts.Runs, expectedRuns) || !reflect.DeepEqual(facts.Messages, expectedMessages) {
		t.Fatalf("runs or messages = %s", facts)
	}
	var rendered coordinatorWakeFacts
	testutil.FailErr(t, "decode reported facts", json.Unmarshal([]byte(facts.String()), &rendered))
	if !reflect.DeepEqual(rendered, facts) {
		t.Fatalf("rendered facts = %+v want %+v", rendered, facts)
	}
	if strings.Contains(facts.String(), "private diagnostic input") {
		t.Fatal("report exposed transcript, input or workflow content")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	facts = collectCoordinatorWakeFacts(canceled, h, sess.ID, receipt.ID, "", context.Canceled)
	for _, source := range []string{"session", "submission", "turn", "workflow", "runs", "human_approval", "host_obligation", "transcript"} {
		if facts.ReadErrors[source] == "" {
			t.Errorf("missing %s read error: %s", source, facts)
		}
	}
	if facts.SessionStatus != nil || facts.SubmissionStatus != nil || facts.TurnStatus != nil || facts.RunStatus != nil ||
		facts.PendingUserInput != nil || facts.HumanApprovalAwaiting != nil || facts.HostObligationHeld != nil || facts.TranscriptRoles != nil || facts.Runs != nil || facts.Messages != nil ||
		facts.AdmissionError != context.Canceled.Error() {
		t.Fatalf("unreadable facts reported as known: %s", facts)
	}
}
