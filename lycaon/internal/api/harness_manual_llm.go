package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	wire "github.com/lycaon/lycaon/pkg/api"
)

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

func (s *Server) handleHarnessLLMPending(w http.ResponseWriter, r *http.Request) {
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
	Error     string          `json:"error"`
}

func (s *Server) handleHarnessLLMRespond(w http.ResponseWriter, r *http.Request) {
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
		var streamErr error
		if c.Error != "" {
			streamErr = errors.New(c.Error)
		}
		chunks = append(chunks, modelcall.StreamChunk{
			Content:   c.Content,
			ToolCalls: c.ToolCalls,
			Done:      c.Done,
			Progress:  c.Progress,
			Err:       streamErr,
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

func (s *Server) handleHarnessLLMAuto(w http.ResponseWriter, r *http.Request) {
	var req harnessAutoReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	s.manualLLM.SetAuto(req.Enabled, req.Text)
	httpio.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true, "enabled": req.Enabled})
}

