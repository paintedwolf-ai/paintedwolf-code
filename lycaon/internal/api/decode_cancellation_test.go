package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
)

func TestCanceledBodyReadIsNotMalformedJSON(t *testing.T) {
	srv := newTestServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	srv.responses.DecodeError(response, request, io.ErrUnexpectedEOF)
	if response.Code != httpio.StatusClientClosedRequest || response.Body.Len() != 0 {
		t.Fatalf("canceled request became an error notice: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	srv.responses.DecodeError(response, request.WithContext(t.Context()), io.ErrUnexpectedEOF)
	assertErrorResponse(t, response, http.StatusBadRequest, "invalid_json")
}

func TestCanceledTrustReadIsNotInventoryFailure(t *testing.T) {
	srv := newTestServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/v1/projects/project/trust", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	srv.Project.WriteProjectInventoryError(response, request, "project", context.Canceled)
	if response.Code != httpio.StatusClientClosedRequest || response.Body.Len() != 0 {
		t.Fatalf("canceled trust read became a notice: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	srv.Project.WriteProjectInventoryError(response, request.WithContext(t.Context()), "project", io.ErrUnexpectedEOF)
	assertErrorResponse(t, response, http.StatusServiceUnavailable, "project_inventory_unavailable")
}
