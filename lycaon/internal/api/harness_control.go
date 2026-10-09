package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/visual"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// registerHarnessRoutes registers authenticated controls for an isolated harness.
func (s *HarnessControl) registerHarnessRoutes() {
	s.harness = s.HarnessPreparation.requireHarnessServices()
	s.router.Route("/harness", func(r chi.Router) {
		r.Use(s.requireClientAuth)
		s.HarnessPreparation.registerHarnessPreparation(r)
		r.Post("/llm/provider-probe", s.HarnessProviders.handleHarnessProviderProbe)
		r.Post("/llm/provider-tools", s.HarnessProviders.handleHarnessProviderTools)
		if s.manualLLM != nil {
			r.Get("/llm/pending", s.handleHarnessLLMPending)
			r.Post("/llm/respond", s.handleHarnessLLMRespond)
			r.Post("/llm/auto", s.handleHarnessLLMAuto)
		}
		r.Post("/overlays", s.HarnessPreparation.handleHarnessOverlays)
		r.Post("/upgrade-history", s.HarnessPreparation.handleHarnessUpgradeHistory)
		r.Post("/transcript", s.handleHarnessTranscript)
		r.Post("/preview", s.handleHarnessPreview)
		r.Post("/sessions/untrusted-content", s.handleHarnessUntrustedContent)
		r.Post("/checkpoints/tool_approval", s.handleHarnessToolApprovalCheckpoint)
		r.Post("/ask_user", s.handleHarnessAskUser)
		r.Get("/ask_user/last_response", s.handleHarnessAskUserLastResponse)
		r.Post("/visual_fixture", s.handleHarnessVisualFixture)
		r.Post("/review_loop_verdict", s.handleHarnessReviewLoopVerdict)
	})
}

// handleHarnessPreview publishes one preview event through project SSE.
func (s *HarnessControl) handleHarnessPreview(w http.ResponseWriter, r *http.Request) {
	var event wire.PreviewEvent
	if err := httpio.DecodeJSON(w, r, &event); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	event.SessionID = strings.TrimSpace(event.SessionID)
	event.PageID = strings.TrimSpace(event.PageID)
	if _, err := uuid.Parse(event.SessionID); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session_id must be a UUID")
		return
	}
	if event.PageID == "" || event.Op == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "page_id and op are required")
		return
	}
	sess, ok := requestscope.Session(s.sessionStore, s.responses, w, r, event.SessionID)
	if !ok {
		return
	}
	s.eventPublisher.PublishPreview(r.Context(), sess.ProjectID, event.SessionID, event)
	httpio.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type harnessUntrustedContentReq struct {
	SessionID string `json:"session_id"`
}

// handleHarnessUntrustedContent records durable untrusted-content provenance.
func (s *HarnessControl) handleHarnessUntrustedContent(w http.ResponseWriter, r *http.Request) {
	var req harnessUntrustedContentReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if _, err := uuid.Parse(sessionID); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session_id must be a UUID")
		return
	}
	if err := s.harness.seeder.SeedUntrustedContent(r.Context(), sessionID); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
	})
}

type harnessTranscriptReq struct {
	SessionID string         `json:"session_id"`
	Messages  []wire.Message `json:"messages"`
}

// handleHarnessTranscript persists fixture rows through the real transcript and SSE path.
func (s *HarnessControl) handleHarnessTranscript(w http.ResponseWriter, r *http.Request) {
	var req harnessTranscriptReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if _, err := uuid.Parse(sessionID); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session_id must be a UUID")
		return
	}
	if len(req.Messages) == 0 || len(req.Messages) > 1_000 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "messages must contain 1 to 1000 rows")
		return
	}
	if err := s.sessions.Transcript.AppendPlain(
		r.Context(),
		sessionID,
		s.stampActiveRun(r, sessionID, req.Messages)...,
	); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
		"count":      len(req.Messages),
	})
}

// stampActiveRun assigns seeded messages to the active workflow span.
func (s *HarnessControl) stampActiveRun(r *http.Request, sessionID string, msgs []wire.Message) []wire.Message {
	run, err := s.Workflow.GetActive(r.Context(), sessionID)
	if err != nil || run == nil {
		return msgs
	}
	for i := range msgs {
		if strings.TrimSpace(msgs[i].WorkflowRunID) == "" {
			msgs[i].WorkflowRunID = run.ID
		}
	}
	return msgs
}

type harnessPendingMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls []wire.ToolCall `json:"tool_calls,omitempty"`
}

type harnessPendingDTO struct {
	Pending   bool                    `json:"pending"`
	ID        string                  `json:"id,omitempty"`
	SessionID string                  `json:"session_id,omitempty"`
	Model     string                  `json:"model,omitempty"`
	Tools     []string                `json:"tools,omitempty"`
	Messages  []harnessPendingMessage `json:"messages,omitempty"`
}

func (s *HarnessControl) handleHarnessLLMPending(w http.ResponseWriter, r *http.Request) {
	wait := time.Duration(0)
	if v := r.URL.Query().Get("wait"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			wait = time.Duration(min(ms, 30_000)) * time.Millisecond
		}
	}
	pend, ok := s.manualLLM.Pending(r.Context(), strings.TrimSpace(r.URL.Query().Get("session_id")), wait)
	if !ok {
		httpio.WriteJSON(w, http.StatusOK, harnessPendingDTO{Pending: false})
		return
	}
	dto := harnessPendingDTO{Pending: true, ID: pend.ID, SessionID: pend.SessionID, Model: pend.Model}
	for _, t := range pend.Tools {
		dto.Tools = append(dto.Tools, t.Name)
	}
	for _, m := range pend.Messages {
		dto.Messages = append(dto.Messages, harnessPendingMessage{
			Role:      string(m.Role),
			Content:   m.Content,
			ToolCalls: m.ToolCalls,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, dto)
}

type harnessRespondReq struct {
	ID        string          `json:"id"`
	Content   string          `json:"content"`
	ToolCalls []wire.ToolCall `json:"tool_calls"`
	// StreamChunks preserve split deltas, progress frames, and truncated arguments.
	StreamChunks []harnessStreamChunk `json:"stream_chunks"`
}

type harnessStreamChunk struct {
	Content   string          `json:"content"`
	ToolCalls []wire.ToolCall `json:"tool_calls"`
	Done      bool            `json:"done"`
	Progress  bool            `json:"progress"`
}

func (s *HarnessControl) handleHarnessLLMRespond(w http.ResponseWriter, r *http.Request) {
	var req harnessRespondReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "id is required")
		return
	}
	var chunks []modelcall.StreamChunk
	for _, c := range req.StreamChunks {
		chunks = append(chunks, modelcall.StreamChunk{
			Content:   c.Content,
			ToolCalls: c.ToolCalls,
			Done:      c.Done,
			Progress:  c.Progress,
		})
	}
	if err := s.manualLLM.RespondWithChunks(req.ID, req.Content, req.ToolCalls, chunks); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeManualLlmRequestNotFound, "manual LLM request is not pending")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type harnessAutoReq struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text"`
}

func (s *HarnessControl) handleHarnessLLMAuto(w http.ResponseWriter, r *http.Request) {
	var req harnessAutoReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	s.manualLLM.SetAuto(req.Enabled, req.Text)
	httpio.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true, "enabled": req.Enabled})
}

type harnessAskUserReq struct {
	SessionID    string   `json:"session_id"`
	Prompt       string   `json:"prompt"`
	ResponseType string   `json:"response_type"`
	Purpose      string   `json:"purpose"`
	Options      []string `json:"options"`
	Artifacts    []string `json:"artifacts"`
}

func (s *HarnessControl) handleHarnessAskUser(w http.ResponseWriter, r *http.Request) {
	mgr := s.Workflow
	var req harnessAskUserReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session_id is required")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "REST or GraphQL?"
	}
	// An omitted type lets the host choose it from the purpose and artifacts, as for ask_user.
	rt := workflowdef.FeedbackResponseType(strings.TrimSpace(req.ResponseType))
	artifacts := make([]string, 0, len(req.Artifacts))
	for _, a := range req.Artifacts {
		if id := strings.TrimSpace(a); id != "" {
			artifacts = append(artifacts, id)
		}
	}
	toolCallID := "harness-ask-" + uuid.NewString()
	assistantID := "harness-a-" + uuid.NewString()
	toolMsgID := "harness-tr-" + uuid.NewString()
	pendingBody := `{"status":"pending","phase_id":""}`
	msgs := []wire.Message{
		{
			ID:   assistantID,
			Role: wire.MessageRoleAssistant,
			ToolCalls: []wire.ToolCall{{
				ID:   toolCallID,
				Name: "ask_user",
				Args: harnessAskUserArgs(prompt, req.Purpose, artifacts),
			}},
		},
		harnessToolResultMessage(toolMsgID, assistantID, toolCallID, "ask_user", pendingBody),
	}
	if err := s.sessions.Transcript.AppendPlain(r.Context(), sessionID, s.stampActiveRun(r, sessionID, msgs)...); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	handle, err := mgr.Asks.RequestUserInput(r.Context(), sessionID, workflowinputs.UserInputRequest{
		Prompt:       prompt,
		ResponseType: rt,
		Purpose:      req.Purpose,
		Options:      append([]string(nil), req.Options...),
		Artifacts:    artifacts,
		ToolCallID:   toolCallID,
	})
	if err != nil {
		reject := &workflowinputs.AskUserReject{}
		if errors.As(err, &reject) {
			s.responses.FailDetails(w, wire.ApiErrorCodeAskUserRejected, map[string]any{"reject_code": reject.Code}, reject.Error())
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	// Announce the card after its transcript rows are durable.
	mgr.Asks.AnnouncePendingAsk(r.Context(), sessionID)
	// Publish the card to late SSE subscribers.
	if msgs, err := s.sessionStore.GetMessages(r.Context(), sessionID); err == nil {
		for _, msg := range msgs {
			if msg.WorkflowFeedback != nil && msg.WorkflowFeedback.PhaseID == handle.PhaseID {
				sess, _ := s.sessionStore.Get(r.Context(), sessionID)
				key := ""
				if sess != nil {
					key = sess.ProjectID
				}
				if key != "" {
					s.eventPublisher.PublishMessageAppend(r.Context(), key, sessionID, msg)
				}
				break
			}
		}
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"session_id":    sessionID,
		"phase_id":      handle.PhaseID,
		"tool_call_id":  toolCallID,
		"prompt":        handle.Prompt,
		"response_type": string(handle.ResponseType),
		"note":          "pending ask_user WorkflowFeedbackCard",
	})
}

func harnessAskUserArgs(prompt, purpose string, artifacts []string) map[string]any {
	args := map[string]any{"prompt": prompt, "artifacts": artifacts}
	if purpose = strings.TrimSpace(purpose); purpose != "" {
		args["purpose"] = purpose
	}
	return args
}

// harnessToolResultMessage keeps synthetic tool receipts attached to their durable call row.
func harnessToolResultMessage(id, assistantID, toolCallID, tool, content string) wire.Message {
	return wire.Message{
		ID:      id,
		Role:    wire.MessageRoleTool,
		Content: content,
		ToolResult: &wire.ToolResult{
			Tool:               tool,
			ToolCallID:         toolCallID,
			AssistantMessageID: assistantID,
			Content:            content,
		},
	}
}

type harnessReviewLoopVerdictReq struct {
	SessionID string            `json:"session_id"`
	Verdict   map[string]string `json:"verdict"`
}

// handleHarnessReviewLoopVerdict seeds a terminal review-loop verdict.
func (s *HarnessControl) handleHarnessReviewLoopVerdict(w http.ResponseWriter, r *http.Request) {
	mgr := s.Workflow
	var req harnessReviewLoopVerdictReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session_id is required")
		return
	}
	if len(req.Verdict) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "verdict is required")
		return
	}
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(r.Context(), sessionID, req.Verdict, nil, nil); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	run, err := mgr.Store.Runs.Store.Runs.ActiveBySession(r.Context(), sessionID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	phase := ""
	runID := ""
	if run != nil {
		phase = run.CurrentPhase
		runID = run.ID
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"session_id":    sessionID,
		"run_id":        runID,
		"current_phase": phase,
	})
}

// handleHarnessAskUserLastResponse peeks the answered ask_user tool row.
func (s *HarnessControl) handleHarnessAskUserLastResponse(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session_id is required")
		return
	}
	msgs, err := s.sessionStore.GetMessages(r.Context(), sessionID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != wire.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if strings.TrimSpace(msg.ToolResult.Tool) != "ask_user" {
			continue
		}
		var body workflowinputs.AskUserAnswerBody
		if err := json.Unmarshal([]byte(msg.ToolResult.Content), &body); err != nil || body.Status != "answered" {
			continue
		}
		httpio.WriteJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"present":       true,
			"source":        "tool_result",
			"phase_id":      body.PhaseID,
			"response":      body.Response,
			"choices":       body.Choices,
			"response_type": body.ResponseType,
			"purpose":       body.Purpose,
			"resolved_by":   body.ResolvedBy,
		})
		return
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "present": false})
}

type harnessVisualFixtureReq struct {
	SessionID string `json:"session_id"`
	Caption   string `json:"caption"`
}

// handleHarnessVisualFixture seeds a 1×1 PNG into the session-tree visual store.
func (s *HarnessControl) handleHarnessVisualFixture(w http.ResponseWriter, r *http.Request) {
	var req harnessVisualFixtureReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session_id is required")
		return
	}
	root := sessiontree.RootID(r.Context(), s.sessionStore, sessionID)
	caption := strings.TrimSpace(req.Caption)
	if caption == "" {
		caption = "harness fixture"
	}
	art, err := s.visualStore.Put(r.Context(), root, visual.Entry{
		Meta: wire.VisualArtifact{
			Mime:    "image/png",
			Source:  wire.VisualArtifactSourceRender,
			Caption: caption,
		},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"artifact_id": art.ID,
		"session_id":  sessionID,
		"root_id":     root,
		"caption":     caption,
	})
}
