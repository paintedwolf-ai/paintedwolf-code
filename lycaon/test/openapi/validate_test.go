package openapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadBundledOpenAPI(t *testing.T) {
	v, err := load()
	testutil.FailErr(t, "load failed", err)
	if v.doc == nil || v.router == nil {
		t.Fatal("expected document and router")
	}
	route, _, _, err := v.findRoute(http.MethodPost, "/v1/sessions", nil)
	if err != nil {
		t.Fatalf("POST /v1/sessions: %v", err)
	}
	if route.Operation.OperationID != "createSession" {
		t.Fatalf("operationId = %q", route.Operation.OperationID)
	}
}

func TestValidateFixtureSessionJSON(t *testing.T) {
	body := []byte(`{
		"id": "11111111-1111-4111-8111-111111111111",
		"owner_person_id": "00000000-0000-4000-8000-000000000002",
		"project_id": "00000000-0000-4000-8000-000000000001",
		"posture": "build",
		"status": "idle",
		"created_at": "2026-01-01T00:00:00Z",
		"activity_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z"
	}`)
	err := ValidateResponse(context.Background(), http.MethodPost, "/v1/sessions", nil, http.StatusAccepted, http.Header{
		"Content-Type": []string{"application/json"},
	}, body)
	testutil.FailErr(t, "ValidateResponse failed", err)
}

func TestValidateRejectsExtraProperties(t *testing.T) {
	body := []byte(`{
		"id": "11111111-1111-4111-8111-111111111111",
		"owner_person_id": "00000000-0000-4000-8000-000000000002",
		"project_id": "00000000-0000-4000-8000-000000000001",
		"posture": "build",
		"status": "idle",
		"created_at": "2026-01-01T00:00:00Z",
		"activity_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"unexpected_field": true
	}`)
	err := ValidateResponse(context.Background(), http.MethodPost, "/v1/sessions", nil, http.StatusAccepted, http.Header{
		"Content-Type": []string{"application/json"},
	}, body)
	if err == nil {
		t.Fatal("expected validation error for extra property")
	}
}
