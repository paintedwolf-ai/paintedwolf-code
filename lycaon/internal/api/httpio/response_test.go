package httpio

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func decodeErrorBody(t *testing.T, w *httptest.ResponseRecorder) wire.ErrorResponse {
	t.Helper()
	var body wire.ErrorResponse
	testutil.FailErr(t, "decode error envelope", json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func TestFailDerivesStatusFromCode(t *testing.T) {
	responder := &Responder{}
	for _, code := range []wire.ApiErrorCode{
		wire.ApiErrorCodeSessionNotFound,
		wire.ApiErrorCodeInvalidRequest,
		wire.ApiErrorCodeProjectMutationInProgress,
		wire.ApiErrorCodeBodyTooLarge,
	} {
		w := httptest.NewRecorder()
		responder.Fail(w, code, "diagnostic")
		if w.Code != code.HTTPStatus() {
			t.Fatalf("%s answered %d, want %d", code, w.Code, code.HTTPStatus())
		}
		body := decodeErrorBody(t, w)
		if body.Code != code || body.Message != "diagnostic" {
			t.Fatalf("%s envelope = %+v", code, body)
		}
	}
}

func TestFailReasonCarriesReasonInDetails(t *testing.T) {
	w := httptest.NewRecorder()
	(&Responder{}).FailReason(w, wire.ApiErrorCodeInvalidRequest, "name is required")
	body := decodeErrorBody(t, w)
	if w.Code != http.StatusBadRequest || body.Details["reason"] != "name is required" {
		t.Fatalf("status %d envelope %+v", w.Code, body)
	}
}

func TestInvalidQueryNamesParam(t *testing.T) {
	w := httptest.NewRecorder()
	(&Responder{}).InvalidQuery(w, &QueryParameterError{Parameter: "limit", Reason: "must be an integer"})
	body := decodeErrorBody(t, w)
	if w.Code != http.StatusBadRequest || body.Code != wire.ApiErrorCodeInvalidQuery || body.Details["param"] != "limit" {
		t.Fatalf("status %d envelope %+v", w.Code, body)
	}
}

func TestDecodeErrorSeparatesTypeErrorsFromMalformedJSON(t *testing.T) {
	responder := &Responder{Logger: slog.New(slog.DiscardHandler)}
	var dst struct {
		Limit int `json:"limit"`
	}
	typeErr := DecodeStrictJSON(strings.NewReader(`{"limit":"ten"}`), &dst)
	w := httptest.NewRecorder()
	responder.DecodeError(w, httptest.NewRequest(http.MethodPost, "/v1/x", nil), typeErr)
	body := decodeErrorBody(t, w)
	if body.Code != wire.ApiErrorCodeInvalidRequest || body.Details["field"] != "limit" {
		t.Fatalf("type error envelope = %+v", body)
	}

	malformed := DecodeStrictJSON(strings.NewReader(`{"limit":`), &dst)
	w = httptest.NewRecorder()
	responder.DecodeError(w, httptest.NewRequest(http.MethodPost, "/v1/x", nil), malformed)
	if body := decodeErrorBody(t, w); body.Code != wire.ApiErrorCodeInvalidJson {
		t.Fatalf("malformed envelope = %+v", body)
	}
}
