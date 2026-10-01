package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type harnessProviderProbeRequest struct {
	AllowLive bool   `json:"allow_live"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Role      string `json:"role"`
}

func (s *Server) handleHarnessProviderProbe(w http.ResponseWriter, r *http.Request) {
	var req harnessProviderProbeRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Role == "" {
		req.Role = llm.PolicySlotCoordinator
	}
	if !req.AllowLive || req.Provider == "" || req.Model == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Explicit live authorization, provider and model are required")
		return
	}
	switch req.Role {
	case llm.PolicySlotCoordinator, llm.PolicySlotAgentPool, llm.PolicySlotLite:
	default:
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "A supported model role is required")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.probeProviderConversation(r.Context(), req))
}

func (s *Server) probeProviderConversation(ctx context.Context, req harnessProviderProbeRequest) map[string]any {
	result := map[string]any{"provider": req.Provider, "model": req.Model, "role": req.Role, "accepted": false, "stages": []llm.ConversationProbe{}}
	ref := llm.ModelRef{ProviderID: req.Provider, Model: req.Model}
	if err := s.llmSvc.ValidateModelRef(ctx, ref, req.Role); err != nil {
		result["code"], result["failure_kind"], result["retryable"] = "model_assignment_rejected", "application", false
		var unavailable *llm.ModelCatalogUnavailableError
		if errors.As(err, &unavailable) {
			result["code"], result["failure_kind"], result["retryable"] = wire.ApiErrorCodeProviderCatalogUnavailable, "provider", true
		}
		result["diagnostic"] = observability.RedactCaptureText(err.Error())
		return result
	}
	stages, err := s.probeProviderRequests(ctx, req)
	result["stages"], result["accepted"] = stages, err == nil
	result["costs"] = estimateProbeCosts(ctx, s.costTracker, req.Provider, req.Model, stages)
	var contextLength *int
	for _, entry := range s.llmSvc.Registry.EffectiveModels(ctx, req.Provider) {
		if entry.ID == req.Model && entry.ContextLength > 0 {
			value := entry.ContextLength
			contextLength = &value
			break
		}
	}
	result["catalog_context_length"] = contextLength
	if err != nil {
		recordProviderProbeFailure(result, err)
	}
	return result
}

func recordProviderProbeFailure(result map[string]any, err error) {
	result["diagnostic"] = observability.RedactCaptureText(err.Error())
	code := wire.NoticeCode("provider_probe_incomplete")
	var notice interface{ NoticeCode() wire.NoticeCode }
	if errors.As(err, &notice) {
		code = notice.NoticeCode()
	}
	result["code"], result["failure_kind"] = code, "application"
	if notice != nil {
		result["failure_kind"] = "provider"
	}
	switch code {
	case wire.NoticeCodeProviderRateLimited, wire.NoticeCodeProviderOverloaded, wire.NoticeCodeProviderServerError,
		wire.NoticeCodeProviderUnreachable, wire.NoticeCodeProviderSilent, wire.NoticeCodeProviderEmptyCompletion:
		result["retryable"] = true
	default:
		result["retryable"] = false
	}
}

func (s *Server) probeProviderRequests(ctx context.Context, req harnessProviderProbeRequest) ([]llm.ConversationProbe, error) {
	if req.Role == llm.PolicySlotLite {
		return llm.ProbeUtility(ctx, func(ctx context.Context, request modelcall.CompletionRequest) (*modelcall.Completion, error) {
			provider, err := s.llmSvc.Registry.Get(req.Provider)
			if err != nil {
				return nil, err
			}
			request.Model = req.Model
			request.Messages = transcript.Project(request.Messages)
			return provider.Complete(ctx, request)
		})
	}
	selection := &llm.ModelSelection{ProviderID: req.Provider, Model: req.Model}
	return llm.ProbeConversation(ctx, func(ctx context.Context, request modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
		return s.llmSvc.Registry.StreamWithSelection(ctx, selection, request)
	})
}
