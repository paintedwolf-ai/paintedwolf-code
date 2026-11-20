package hostcontracts

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func capabilityAuthorityGrants(t *testing.T, base string) []wire.ApprovalGrant {
	t.Helper()
	resp, err := contractfixture.AuthedHTTPGet(base + "/v1/approval-grants")
	testutil.FailErr(t, "list approval grants", err)
	defer resp.Body.Close()
	var out wire.ApprovalGrantsResponse
	testutil.FailErr(t, "decode approval grants", json.NewDecoder(resp.Body).Decode(&out))
	return out.Grants
}

// A settings socket grant names one absolute socket, and a project-scoped one
// names its project; each malformed request is refused with its typed code and
// saves nothing, while a project grant needs no folder.
func TestCreateSocketGrantRequiresOneAbsoluteSocketAndProjectID(t *testing.T) {
	_, base, _ := contractfixture.NewSettingsTestServer(t)
	dir, err := os.MkdirTemp("/tmp", "lyc-grant-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "svc.sock")
	ln, err := net.Listen("unix", sock)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })

	post := func(body map[string]any) *http.Response {
		t.Helper()
		raw, err := json.Marshal(body)
		testutil.FailErr(t, "encode grant request", err)
		resp, err := contractfixture.AuthedHTTPPost(base+"/v1/approval-grants", "application/json", string(raw))
		testutil.FailErr(t, "post approval grant", err)
		return resp
	}
	for name, tc := range map[string]struct {
		body map[string]any
		code wire.ApiErrorCode
	}{
		"relative path":         {map[string]any{"scope": "device", "category": "socket_path", "socket_path": "svc.sock"}, wire.ApiErrorCodeInvalidRequest},
		"path list":             {map[string]any{"scope": "device", "category": "socket_path", "socket_path": sock, "socket_paths": []string{sock}}, wire.ApiErrorCodeInvalidJson},
		"project without an id": {map[string]any{"scope": "project", "category": "socket_path", "socket_path": sock}, wire.ApiErrorCodeInvalidRequest},
	} {
		resp := post(tc.body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var refusal wire.ErrorResponse
		if resp.StatusCode != http.StatusBadRequest || json.Unmarshal(raw, &refusal) != nil || refusal.Code != tc.code {
			t.Errorf("%s: status %d body %s, want 400 %s", name, resp.StatusCode, raw, tc.code)
		}
		if grants := capabilityAuthorityGrants(t, base); len(grants) != 0 {
			t.Fatalf("%s: refused request saved grants: %+v", name, grants)
		}
	}

	resp := post(map[string]any{"scope": "project", "category": "socket_path", "socket_path": sock, "project_id": "proj-unfoldered"})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("project socket grant status %d body %s, want 201", resp.StatusCode, raw)
	}
	grants := capabilityAuthorityGrants(t, base)
	if len(grants) != 1 || grants[0].ProjectID != "proj-unfoldered" || grants[0].ProjectDir != "" || grants[0].ApprovedPath != sock {
		t.Fatalf("saved project socket grant = %+v, want one grant for proj-unfoldered at %s", grants, sock)
	}
}
