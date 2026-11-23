package modeladmin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/llm/policytest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestEffectivePolicyResolvesInheritedLiteWithoutChangingOverlay(t *testing.T) {
	remote := llm.ModelRef{ProviderID: "remote", Model: "chosen"}
	handler := &Handler{service: &llm.Service{Policy: policytest.New(t, llm.ModelPolicy{Coordinator: remote}), Registry: &llm.Registry{}}}
	for _, effective := range []bool{true, false} {
		path := "/v1/settings/model-policy"
		if effective {
			path += "?effective=true"
		}
		response := httptest.NewRecorder()
		handler.GetPolicy(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("response: %s", response.Body.String())
		}
		var policy wire.ModelPolicy
		testutil.FailErr(t, "decode policy", json.Unmarshal(response.Body.Bytes(), &policy))
		if effective && (policy.Lite == nil || policy.Lite.ProviderID != remote.ProviderID) {
			t.Fatalf("effective lite = %+v", policy.Lite)
		}
		if !effective && policy.Lite != nil {
			t.Fatalf("overlay acquired default: %+v", policy.Lite)
		}
	}
	if handler.Registry() == nil {
		t.Fatal("expected non-nil registry")
	}
	rec := httptest.NewRecorder()
	handler.DeleteLocalModel(rec, httptest.NewRequest(http.MethodDelete, "/v1/providers/other/local-model", nil))
	if rec.Code == http.StatusNoContent {
		t.Fatalf("unexpected 204 for unknown provider")
	}
	errRec := httptest.NewRecorder()
	handler.modelOperationResponse(errRec, errors.New("invalid local model"))
	if errRec.Code == http.StatusOK {
		t.Fatalf("expected error status from modelOperationResponse")
	}
	okRec := httptest.NewRecorder()
	handler.modelOperationResponse(okRec, nil)
	if okRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from modelOperationResponse, got %d", okRec.Code)
	}

	withProvider := func(req *http.Request, id string) *http.Request {
		req.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider_id", id)
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	handler.SelectLocalModel(httptest.NewRecorder(), withProvider(httptest.NewRequest(http.MethodPut, "/v1/providers/other/local-model", strings.NewReader(`{"model":"m1"}`)), "other"))
	handler.SelectLocalModel(httptest.NewRecorder(), withProvider(httptest.NewRequest(http.MethodPut, "/v1/providers/local/local-model", strings.NewReader(`{"model":"m1"}`)), llm.IncludedProviderID))
	_ = handler.ImportLocalModel(t.Context(), "m1", "/tmp/bundle")
}
