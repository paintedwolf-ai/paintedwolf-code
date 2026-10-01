package contract

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

func stubElevatedAccessCall[T any](t *testing.T, server *httptest.Server, method, id, suffix string, status int) T {
	t.Helper()
	path := "/v1/sessions/" + id + "/elevated-access" + suffix
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
	contractcheck.FailErr(t, "create elevated access request", err)
	response, err := server.Client().Do(req)
	contractcheck.FailErr(t, "send elevated access request", err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	contractcheck.FailErr(t, "read elevated access response", err)
	if response.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.StatusCode, status, data)
	}
	contractcheck.FailErr(t, "validate elevated access response", openapi.ValidateResponse(t.Context(), method, "/v1/sessions/{id}/elevated-access"+suffix, map[string]string{"id": id}, response.StatusCode, response.Header, data))
	var result T
	contractcheck.FailErr(t, "decode elevated access response", json.Unmarshal(data, &result))
	return result
}

func TestStubElevatedAccessRevocation(t *testing.T) {
	t.Parallel()
	server := NewStubServer()
	t.Cleanup(server.Close)
	before := stubElevatedAccessCall[api.ElevatedAccessSummary](t, server, http.MethodGet, fixtureSessionID, "", http.StatusOK)
	if !before.ApprovalsEnabled || before.RootSessionID != fixtureSessionID || before.Total != 2 || len(before.Records) != before.Total || !reflect.DeepEqual(before.SharedScopes, []api.ApprovalGrantScope{api.ApprovalGrantScopeDevice}) {
		t.Fatalf("initial elevated access = %+v", before)
	}
	revoked := stubElevatedAccessCall[api.RevokeElevatedAccessResponse](t, server, http.MethodPost, fixtureSessionID, "/revoke", http.StatusOK)
	if len(revoked.Results) != len(before.Records) {
		t.Fatalf("revocation results = %+v", revoked.Results)
	}
	for i, result := range revoked.Results {
		if result.ID != before.Records[i].ID || result.Disposition != "revoked" {
			t.Errorf("revocation result = %+v, record = %+v", result, before.Records[i])
		}
	}
	after := stubElevatedAccessCall[api.ElevatedAccessSummary](t, server, http.MethodGet, fixtureSessionID, "", http.StatusOK)
	if !reflect.DeepEqual(after, revoked.Remaining) || after.Total != 0 || len(after.Records) != 0 || len(after.SharedScopes) != 0 || !after.ApprovalsEnabled || after.RootSessionID != fixtureSessionID {
		t.Fatalf("remaining elevated access = %+v, revoke response = %+v", after, revoked.Remaining)
	}
	repeated := stubElevatedAccessCall[api.RevokeElevatedAccessResponse](t, server, http.MethodPost, fixtureSessionID, "/revoke", http.StatusOK)
	if len(repeated.Results) != 0 || !reflect.DeepEqual(repeated.Remaining, after) {
		t.Fatalf("repeated revoke = %+v", repeated)
	}
}

func TestStubElevatedAccessUnknownSession(t *testing.T) {
	t.Parallel()
	server := NewStubServer()
	t.Cleanup(server.Close)
	for _, route := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodPost, "/revoke"}} {
		result := stubElevatedAccessCall[api.ErrorResponse](t, server, route.method, fixtureCheckpointID, route.suffix, http.StatusNotFound)
		if result.Code != api.ApiErrorCodeSessionNotFound {
			t.Fatalf("unknown session error = %+v", result)
		}
	}
	existing := stubElevatedAccessCall[api.ElevatedAccessSummary](t, server, http.MethodGet, fixtureSessionID, "", http.StatusOK)
	if existing.Total != 2 {
		t.Fatalf("unknown session revoke changed existing authority: %+v", existing)
	}
}
