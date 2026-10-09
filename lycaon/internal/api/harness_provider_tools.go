package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type harnessToolFailure struct {
	Code      string `json:"code"`
	Kind      string `json:"kind"`
	Retryable bool   `json:"retryable"`
}

type harnessToolResult struct {
	llm.ToolVerification
	OK                 bool                `json:"ok"`
	ApplicationVersion string              `json:"application_version"`
	CheckRevision      int                 `json:"check_revision"`
	Error              string              `json:"error,omitempty"`
	Failure            *harnessToolFailure `json:"failure,omitempty"`
}

type harnessProviderToolsRequest struct {
	AllowLive bool   `json:"allow_live"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
}

func (s *HarnessProviders) handleHarnessProviderTools(w http.ResponseWriter, r *http.Request) {
	var request harnessProviderToolsRequest
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !request.AllowLive || request.Provider == "" || request.Model == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Explicit live authorization, provider and model are required")
		return
	}
	verification, err := s.llmSvc.Registry.CheckToolCompatibility(r.Context(), request.Provider, request.Model)
	result := harnessToolResult{ToolVerification: verification, OK: err == nil, ApplicationVersion: version.Version, CheckRevision: llm.ToolCompatibilityRevision}
	if err != nil {
		result.Error = observability.RedactCaptureText(err.Error())
		failure := map[string]any{}
		recordProviderProbeFailure(failure, err)
		result.Failure = &harnessToolFailure{Code: fmt.Sprint(failure["code"]), Kind: fmt.Sprint(failure["failure_kind"]), Retryable: failure["retryable"] == true}
		var compatibility *llm.ToolCompatibilityError
		if errors.As(err, &compatibility) {
			result.Failure = &harnessToolFailure{Code: compatibility.Code, Kind: "compatibility"}
		}
		if refusal, ok := providerretry.AsModelRefused(err); ok && refusal.Evidence.Sticky() {
			result.Failure.Kind = "model_unavailable"
		}
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}
