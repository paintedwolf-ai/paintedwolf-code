package capabilityadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRevokeApprovalRemovesEveryProjection(t *testing.T) {
	for _, kind := range []hitl.AuthorityDeltaKind{
		hitl.AuthorityGrantedPath, hitl.AuthorityReadPathChat, hitl.AuthorityWriteRootChat,
		hitl.AuthorityLocalListenChat, hitl.AuthorityLoopbackConnectChat,
		hitl.AuthoritySocketChat, hitl.AuthorityDirectIPChat,
	} {
		t.Run(string(kind), func(t *testing.T) {
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
			testutil.FailErr(t, "create approval store", err)
			gate := settings.NewRuleApprovalGate(store, settings.NoSources())
			handler := New(&httpio.Responder{}, withHost(t, Deps{
				Settings: &settings.Service{Approvals: store}, Gate: gate,
				Authority: Authority{
					GrantedPaths: grantedpath.NewRuntime(),
					ReadPaths:    approvalstate.NewSandboxPathGrantRuntime(),
					WriteRoots:   approvalstate.NewSandboxPathGrantRuntime(),
					Listen:       approvalstate.NewSandboxPortGrantRuntime(),
					Loopback:     approvalstate.NewSandboxPortGrantRuntime(),
					Sockets:      approvalstate.NewSocketCapabilityRuntime(),
					DirectIP:     approvalstate.NewDirectIPCapabilityRuntime(),
				},
			}))
			s := &handler
			path := filepath.Join(t.TempDir(), "outside")
			grant := hitl.ApprovalGrant{
				ID: "grant_shared", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "chat-a",
				Predicate: hitl.ApprovalGrantPredicate{Category: "path", Pattern: path},
			}
			delta := hitl.ApprovalAuthorityDelta{
				Kind: kind, Grant: &grant, ChatSessionID: "chat-a",
				GrantedPath: &hitl.GrantedPathDelta{Path: path, Write: true},
				ReadPaths:   []string{path}, WriteRoots: []string{path},
				ListenPorts: []uint16{19001}, ConnectPorts: []uint16{19002},
				Sockets:       []hitl.ApprovalSocketTarget{{ApprovedPath: path, ResolvedPath: path}},
				DirectIPLease: &hitl.DirectIPLease{ActionDigest: "action", RequestDigest: "request", ConfinementDigest: "confinement"},
			}
			option := hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{
				{Kind: hitl.AuthorityGenericGrant, Grant: &grant}, delta,
			}}
			_, err = s.InstallApprovalOption(t.Context(), "checkpoint-a", option)
			testutil.FailErr(t, "install all projections", err)
			if approvalRuntimeCount(s) != 1 {
				t.Fatal("missing runtime authority before revoke")
			}
			if len(gate.ListGrants("chat-a")) != 1 {
				t.Fatal("missing lease projection before revoke")
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/approval-grants/revoke", strings.NewReader(`{"ids":["grant_shared"]}`))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			s.HandleRevokeApprovalGrants(response, req)
			var result wire.RevokeApprovalGrantsResponse
			testutil.FailErr(t, "decode revoke", json.Unmarshal(response.Body.Bytes(), &result))
			if response.Code != http.StatusOK || len(result.Results) != 1 || !result.Results[0].Revoked {
				t.Fatalf("revoke status %d: %s", response.Code, response.Body.String())
			}
			if len(gate.ListGrants("chat-a")) != 0 {
				t.Fatal("one revocation left the lease active")
			}
			if approvalRuntimeCount(s) != 0 {
				t.Fatal("one revocation left runtime authority active")
			}
		})
	}
}

// fakeChatGrantLedger stands in for the durable chat-grant rows.
type fakeChatGrantLedger struct{ ids map[string]bool }

func (f *fakeChatGrantLedger) ForgetChatGrant(_ context.Context, id string) (bool, error) {
	present := f.ids[id]
	delete(f.ids, id)
	return present, nil
}

// A revoke removes the durable row so a restart cannot restore it, including
// when the runtime already dropped the grant.
func TestRevokeForgetsTheDurableChatGrant(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create approval store", err)
	ledger := &fakeChatGrantLedger{ids: map[string]bool{"grant_recorded": true, "quiet_recorded": true}}
	handler := New(&httpio.Responder{}, withHost(t, Deps{
		Settings: &settings.Service{Approvals: store}, Gate: settings.NewRuleApprovalGate(store, settings.NoSources()),
		Authority: Authority{ChatGrants: ledger},
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/approval-grants/revoke",
		strings.NewReader(`{"ids":["grant_recorded","quiet_recorded","grant_missing"]}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.HandleRevokeApprovalGrants(response, req)
	var result wire.RevokeApprovalGrantsResponse
	testutil.FailErr(t, "decode revoke", json.Unmarshal(response.Body.Bytes(), &result))
	if len(result.Results) != 3 || !result.Results[0].Revoked || !result.Results[1].Revoked || result.Results[2].Revoked {
		t.Fatalf("revoke results = %+v", result.Results)
	}
	if len(ledger.ids) != 0 {
		t.Fatalf("durable rows left after revoke: %v", ledger.ids)
	}
}

// withHost supplies the host services New requires that a test leaves unset.
func withHost(t *testing.T, deps Deps) Deps {
	t.Helper()
	fill := apitestdeps.Deps{ApprovalDecisions: deps.ApprovalDecisions, ApprovalGate: deps.Gate, Settings: deps.Settings, Checkpoints: deps.Checkpoints, Projects: deps.Projects, Store: deps.Store}
	apitestdeps.Fill(t, &fill)
	deps.Checkpoints, deps.Projects, deps.Store = fill.Checkpoints, fill.Projects, fill.Store
	deps.ApprovalDecisions, deps.Gate, deps.Settings = fill.ApprovalDecisions, fill.ApprovalGate, fill.Settings
	return deps
}

func approvalRuntimeCount(s *Handler) int {
	return len(s.GrantedPaths.List("chat-a")) + len(s.ReadPaths.ListChatGrants("chat-a")) +
		len(s.WriteRoots.ListChatGrants("chat-a")) + len(s.Listen.ListChatGrants("chat-a")) +
		len(s.Loopback.ListChatGrants("chat-a")) + len(s.Sockets.ListChatGrants("chat-a")) +
		len(s.DirectIP.ListChatGrants("chat-a"))
}
