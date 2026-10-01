package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

func stubHistoryCall[T any](t *testing.T, server *httptest.Server, method, path string, body any, wantStatus int) T {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		contractcheck.FailErr(t, "encode history request", err)
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bodyReader)
	contractcheck.FailErr(t, "create history request", err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(req)
	contractcheck.FailErr(t, "send history request", err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	contractcheck.FailErr(t, "read history response", err)
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.StatusCode, wantStatus, data)
	}
	contractcheck.FailErr(t, "validate history response schema", openapi.ValidateResponse(t.Context(), method, path, nil, response.StatusCode, response.Header, data))
	var result T
	if len(data) > 0 {
		contractcheck.FailErr(t, "decode history response", json.Unmarshal(data, &result))
	}
	return result
}

func TestStubHistoryRetentionReviewAndPruning(t *testing.T) {
	t.Parallel()
	server := NewStubServer()
	t.Cleanup(server.Close)
	status := stubHistoryCall[api.HistoryStorageStatus](t, server, "GET", "/v1/history-storage", nil, 200)
	if status.Policy.Recordings.Mode != "forever" || status.Policy.Suspended || len(status.Lanes) != 5 {
		t.Fatalf("initial history status = %+v", status)
	}
	request := api.HistoryRetentionRequest{Policy: status.Policy}
	request.Policy.Recordings = api.HistoryRetentionRule{Mode: "max_age", MaxAgeDays: 1}
	stubHistoryCall[api.ErrorResponse](t, server, "PATCH", "/v1/history-storage", request, 409)
	preview := stubHistoryCall[api.HistoryRetentionPreview](t, server, "POST", "/v1/history-storage/preview", request, 200)
	if preview.EligibleCount != 2 || preview.ReclaimableBytes != 2<<20 {
		t.Fatalf("reviewed recordings = %+v", preview)
	}
	request.PreviewToken = preview.Token
	changed := request
	changed.Policy.Recordings.MaxAgeDays = 2
	stubHistoryCall[api.ErrorResponse](t, server, "PATCH", "/v1/history-storage", changed, 409)
	policy := stubHistoryCall[api.HistoryRetentionPolicy](t, server, "PATCH", "/v1/history-storage", request, 200)
	if policy.Revision != status.Policy.Revision+1 || policy.Recordings != request.Policy.Recordings || policy.Suspended {
		t.Fatalf("saved policy = %+v", policy)
	}
	stubHistoryCall[api.ErrorResponse](t, server, "POST", "/v1/history-storage/prune", request, 409)
	request.Policy = policy
	request.PreviewToken = ""
	preview = stubHistoryCall[api.HistoryRetentionPreview](t, server, "POST", "/v1/history-storage/preview", request, 200)
	request.PreviewToken = preview.Token
	first := stubHistoryCall[api.HistoryPruneResult](t, server, "POST", "/v1/history-storage/prune", request, 200)
	if first.RemovedCount != 1 || first.ReleasedBytes != 1<<20 || first.Complete {
		t.Fatalf("first batch = %+v", first)
	}
	stubHistoryCall[api.ErrorResponse](t, server, "POST", "/v1/history-storage/prune", request, 409)
	request.PreviewToken = first.PreviewToken
	last := stubHistoryCall[api.HistoryPruneResult](t, server, "POST", "/v1/history-storage/prune", request, 200)
	if last.RemovedCount != 1 || last.ReleasedBytes != 1<<20 || !last.Complete {
		t.Fatalf("last batch = %+v", last)
	}
	status = stubHistoryCall[api.HistoryStorageStatus](t, server, "GET", "/v1/history-storage", nil, 200)
	for _, lane := range status.Lanes {
		want := int64(2 << 20)
		if lane.ID == "recordings" {
			want = 0
		}
		if lane.StoredBytes != want {
			t.Errorf("lane %s retained bytes = %d, want %d", lane.ID, lane.StoredBytes, want)
		}
	}
}

func TestStubHistoryProtectionInvalidatesReview(t *testing.T) {
	t.Parallel()
	for _, owner := range []api.HistoryProtection{
		{ScopeType: "project", ScopeID: fixtureProjectID, Protected: true},
		{ScopeType: "session", ScopeID: fixtureSessionID, Protected: true},
	} {
		t.Run(owner.ScopeType, func(t *testing.T) {
			t.Parallel()
			server := NewStubServer()
			t.Cleanup(server.Close)
			status := stubHistoryCall[api.HistoryStorageStatus](t, server, "GET", "/v1/history-storage", nil, 200)
			request := api.HistoryRetentionRequest{Policy: status.Policy}
			request.Policy.Checkpoints = api.HistoryRetentionRule{Mode: "max_bytes", MaxBytes: 1 << 20}
			preview := stubHistoryCall[api.HistoryRetentionPreview](t, server, "POST", "/v1/history-storage/preview?project_id="+fixtureProjectID, request, 200)
			if preview.EligibleCount != 1 {
				t.Fatalf("budget preview = %+v", preview)
			}
			request.PreviewToken = preview.Token
			stubHistoryCall[api.HistoryProtection](t, server, "POST", "/v1/history-storage/protections", owner, 201)
			stubHistoryCall[api.ErrorResponse](t, server, "POST", "/v1/history-storage/prune?project_id="+fixtureProjectID, request, 409)
			status = stubHistoryCall[api.HistoryStorageStatus](t, server, "GET", "/v1/history-storage", nil, 200)
			if len(status.Protections) != 1 || status.Protections[0] != owner {
				t.Fatalf("saved protection = %+v", status.Protections)
			}
			preview = stubHistoryCall[api.HistoryRetentionPreview](t, server, "POST", "/v1/history-storage/preview?project_id="+fixtureProjectID, request, 200)
			if preview.EligibleCount != 0 {
				t.Fatalf("protected preview = %+v", preview)
			}
			stubHistoryCall[any](t, server, "DELETE", "/v1/history-storage/protections/"+owner.ScopeID, nil, 204)
			preview = stubHistoryCall[api.HistoryRetentionPreview](t, server, "POST", "/v1/history-storage/preview?project_id="+fixtureProjectID, request, 200)
			if preview.EligibleCount != 1 {
				t.Fatalf("unprotected preview = %+v", preview)
			}
		})
	}
}
