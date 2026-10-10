package httpaction

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type servicePermissionReview struct {
	hitl.CheckpointManager
	t             *testing.T
	authority     hitl.ApprovalGate
	cards         int
	ports         []uint16
	secretGrantID string
}

func (r *servicePermissionReview) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	r.cards++
	plan := req.ApprovalPlan
	if plan == nil {
		var err error
		plan, err = hitl.CompileCheckpointApprovalPlan(req)
		testutil.FailErr(r.t, "compile service review", err)
	}
	option, ok := plan.Option(plan.RecommendedOptionID)
	if !ok || option.Rung != hitl.ApprovalRungChat {
		r.t.Fatal("service review did not recommend task permission")
	}
	for _, delta := range option.Authority {
		switch delta.Kind {
		case hitl.AuthorityGenericGrant:
			_, err := r.authority.ApplyGrant(*delta.Grant)
			testutil.FailErr(r.t, "install reviewed grant", err)
			if delta.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
				r.secretGrantID = delta.Grant.ID
			}
		case hitl.AuthorityLoopbackConnectChat:
			r.ports = append(r.ports, delta.ConnectPorts...)
		default:
			r.t.Fatalf("unexpected service authority %s", delta.Kind)
		}
	}
	return &hitl.CheckpointResponse{CheckpointID: fmt.Sprint(r.cards), Status: hitl.DecisionStatusPending}, nil
}
func (r *servicePermissionReview) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{Status: hitl.DecisionStatusApproved, Result: &hitl.DecisionResult{Approved: true}}, nil
}
func (r *servicePermissionReview) Await(context.Context, tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	r.t.Fatal("approved local service required another capability review")
	return tools.LoopbackConnectResult{}, nil
}
func (r *servicePermissionReview) SessionLoopbackGrant(context.Context, string, string) (bool, []uint16) {
	return len(r.ports) > 0, append([]uint16(nil), r.ports...)
}

func TestSetupPermissionCoversRepeatedAuthenticatedServiceUse(t *testing.T) {
	service, _ := managedRequestService(t)
	const value = "local-service-password"
	meta := hostSecret(t, service, "service-password", "Service password", value)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, password, ok := req.BasicAuth()
		if !ok || password != value {
			t.Error("request did not use the protected password")
		}
		calls++
		_, _ = io.WriteString(w, "authenticated")
	}))
	defer server.Close()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approvals", err)
	authority := settings.NewRuleApprovalGate(store, settings.NoSources())
	review := &servicePermissionReview{t: t, authority: authority}
	registry := tools.NewDefaultRegistry()
	executor := toolexecution.NewExecutor(nil, registry, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), review, authority)
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	executor.Secrets.SetSecretResolver(service)
	matcher := testSecretMatcher(t)
	executor.Secrets.SetSecretMatcher(matcher)
	executor.Capabilities.SetLoopbackConnectGate(review)
	executor.Boundary.SetSessionLoopbackGrant(review.SessionLoopbackGrant)
	testutil.FailErr(t, "register setup consumer", registry.Register("command", func(_ context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if !strings.Contains(args["command"].(string), value) {
			t.Fatal("setup did not receive value")
		}
		if strings.Contains(tc.Effects.CanonicalArgs["command"].(string), value) {
			t.Fatal("canonical setup leaked value")
		}
		return "service configured", nil
	}))
	testutil.FailErr(t, "register HTTP consumer", Register(registry, Deps{Boundary: testBoundary(), SecretMatcher: matcher, SecretAsk: executor.Secrets.AskSecretScreen, Secrets: service}))
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: testdbseed.DefaultProjectID,
			SessionID:  "task",
			ToolCallID: "setup"},
	}
	_, err = executor.Invoke(t.Context(), "command", map[string]any{
		"command":    "setup --password " + meta.Reference,
		"secret_use": map[string]any{"services": []any{server.URL}},
	}, tc)
	testutil.FailErr(t, "approve setup and service", err)
	if review.cards != 1 {
		t.Fatalf("setup required %d cards", review.cards)
	}
	request := map[string]any{"method": "GET", "url": server.URL, "auth": map[string]any{"scheme": "basic", "username": "user", "password": meta.Reference}, "capability_request": loopbackCapability(t, server.URL)}
	for i := range 3 {
		tc.Identity.ToolCallID = fmt.Sprintf("request-%d", i)
		_, err = executor.Invoke(t.Context(), "http_request", request, tc)
		testutil.FailErr(t, "reuse service permission", err)
	}
	if review.cards != 1 || calls != 3 {
		t.Fatalf("cards=%d requests=%d, want one review and three authenticated requests", review.cards, calls)
	}
	removed, err := authority.RevokeGrant(review.secretGrantID)
	testutil.FailErr(t, "revoke disclosure permission", err)
	if !removed {
		t.Fatal("secret permission missing from revocation inventory")
	}
	tc.Identity.ToolCallID = "after-revocation"
	_, err = executor.Invoke(t.Context(), "http_request", request, tc)
	testutil.FailErr(t, "review after revocation", err)
	if review.cards != 2 {
		t.Fatal("revocation did not require fresh secret review")
	}
}
