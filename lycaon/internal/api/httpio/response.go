// Package httpio applies common HTTP request and response contracts.
package httpio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type Responder struct {
	Logger  *slog.Logger
	Notices *usernotice.Catalog
}

func (s *Responder) InternalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && errors.Is(r.Context().Err(), context.Canceled) {
		s.Logger.DebugContext(context.WithoutCancel(r.Context()), "request canceled",
			"path", r.URL.Path, "method", r.Method)
		w.WriteHeader(StatusClientClosedRequest)
		return
	}
	attrs := []any{
		"error", err,
		"path", r.URL.Path,
		"method", r.Method,
	}
	if reqID := middleware.GetReqID(r.Context()); reqID != "" {
		attrs = append(attrs, "request_id", reqID)
	}
	s.Logger.ErrorContext(r.Context(), "internal error", attrs...)
	s.Fail(w, wire.ApiErrorCodeInternalError, "internal server error")
}

func (s *Responder) DecodeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(r.Context().Err(), context.Canceled) {
		w.WriteHeader(StatusClientClosedRequest)
		return
	}
	// Keep decoder diagnostics locally; never echo submitted values to the UI.
	s.Logger.DebugContext(r.Context(), "request body rejected", "method", r.Method,
		"path", r.URL.Path, "request_id", middleware.GetReqID(r.Context()), "error", err)
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, ErrUnsupportedMediaType):
		s.Fail(w, wire.ApiErrorCodeUnsupportedMediaType, "request Content-Type is not supported")
	case errors.Is(err, ErrBodyTooLarge) || IsBodyTooLarge(err):
		s.Fail(w, wire.ApiErrorCodeBodyTooLarge, "request body too large")
	case errors.As(err, &typeErr) && typeErr.Field != "":
		s.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": typeErr.Field}, "request field has the wrong type")
	default:
		s.Fail(w, wire.ApiErrorCodeInvalidJson, "invalid JSON body")
	}
}

// Fail uses the status declared by code and its rendered notice.
func (s *Responder) Fail(w http.ResponseWriter, code wire.ApiErrorCode, message string) {
	s.FailDetails(w, code, nil, message)
}

// FailReason adds a host-authored reason for notice rendering.
func (s *Responder) FailReason(w http.ResponseWriter, code wire.ApiErrorCode, reason string) {
	s.FailDetails(w, code, map[string]any{"reason": reason}, reason)
}

// FailDetails adds structured details for clients and notice rendering.
func (s *Responder) FailDetails(w http.ResponseWriter, code wire.ApiErrorCode, details map[string]any, message string) {
	resp := s.ErrorResponse(code, details, message)
	if len(details) > 0 {
		resp.Details = details
	}
	WriteJSON(w, code.HTTPStatus(), resp)
}

// Unavailable accepts only codes whose declared status is 503.
func (s *Responder) Unavailable(w http.ResponseWriter, code wire.ApiErrorCode, message string) {
	if code.HTTPStatus() != http.StatusServiceUnavailable {
		panic(fmt.Sprintf("httpio: %s does not declare 503", code))
	}
	s.Fail(w, code, message)
}

func (s *Responder) ErrorResponse(
	code wire.ApiErrorCode,
	ctx map[string]any,
	message string,
) wire.ErrorResponse {
	resp := wire.ErrorResponse{Code: code}
	if s != nil && s.Notices != nil {
		copy := s.Notices.RenderWire(string(code), ctx)
		resp.Title = copy.Title
		resp.Message = copy.Message
		resp.SuggestedAction = copy.SuggestedAction
		for _, a := range copy.Actions {
			resp.Actions = append(resp.Actions, wire.NoticeAction(a))
		}
		resp.Retryable = s.Notices.Retryable(string(code))
		if placement, ok := s.Notices.Placement(string(code), ctx); ok {
			resp.Tier = wire.NoticeTier(placement.Tier)
			resp.Scope = wire.NoticeScope(placement.Scope)
			resp.Resolution = placement.ID
		}
		return resp
	}
	resp.Message = message
	return resp
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("writeJSON: encode failed", "err", err)
		status = http.StatusInternalServerError
		body, err = json.Marshal(wire.ErrorResponse{
			Code:    wire.ApiErrorCodeInternalError,
			Message: "the server could not encode its response",
		})
		if err != nil {
			slog.Error("writeJSON: fallback encode failed", "err", err)
			body = []byte(`{"code":"internal_error","message":"the server could not encode its response"}`)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	body = append(body, '\n')
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.Warn("writeJSON: response write failed", "err", err)
	}
}

// InvalidQueryParam rejects one query parameter with host-authored reason copy.
func (s *Responder) InvalidQueryParam(w http.ResponseWriter, param, reason string) {
	s.InvalidQuery(w, &QueryParameterError{Parameter: param, Reason: reason})
}

func (s *Responder) InvalidQuery(w http.ResponseWriter, err error) {
	var queryErr *QueryParameterError
	if !errors.As(err, &queryErr) {
		s.Fail(w, wire.ApiErrorCodeInvalidQuery, "query parameter is invalid")
		return
	}
	s.FailDetails(w, wire.ApiErrorCodeInvalidQuery, map[string]any{
		"param":  queryErr.Parameter,
		"reason": queryErr.Reason,
	}, queryErr.Error())
}

// ModelAssignmentError answers a refused provider and model assignment. The
// refusal's own text stays in the log; it quotes caller input.
func (s *Responder) ModelAssignmentError(w http.ResponseWriter, r *http.Request, err error) {
	var unavailable *llm.ModelCatalogUnavailableError
	if errors.As(err, &unavailable) {
		s.FailDetails(w, wire.ApiErrorCodeProviderCatalogUnavailable,
			map[string]any{"provider_id": unavailable.ProviderID, "model": unavailable.Model}, "Model catalog unavailable")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.InternalError(w, r, err)
		return
	}
	s.Logger.DebugContext(r.Context(), "model assignment refused", "path", r.URL.Path, "err", err)
	s.FailReason(w, wire.ApiErrorCodeInvalidRequest, "the provider and model cannot be assigned to this role")
}

// RequireJSONObjectKeys rejects bodies that omit top-level keys required by OpenAPI.
func (s *Responder) RequireJSONObjectKeys(w http.ResponseWriter, raw map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := raw[k]; !ok {
			s.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{
				"field":  k,
				"reason": fmt.Sprintf("missing required field %q", k),
			}, fmt.Sprintf("missing required field %q", k))
			return false
		}
	}
	return true
}

// InvalidField rejects one request body field with host-authored reason copy.
func (s *Responder) InvalidField(w http.ResponseWriter, field, reason string) {
	s.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": field, "reason": reason}, field+" "+reason)
}

// RequireJSONObjectAnyKey rejects a body that includes none of the named keys.
func (s *Responder) RequireJSONObjectAnyKey(w http.ResponseWriter, raw map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := raw[k]; ok {
			return true
		}
	}
	s.Fail(w, wire.ApiErrorCodeInvalidRequest, "request must include at least one field")
	return false
}
