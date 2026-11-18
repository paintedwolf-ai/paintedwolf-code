package toolexecution_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// A model's tool call is the agent's effect, never the requesting person's:
// the owner sees no caller even when the request that started the turn had one.
func TestAgentToolCallsCarryNoRequestCaller(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	var sawCaller, invoked bool
	testutil.FailErr(t, "register read owner", reg.Register("read", func(ctx context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		invoked = true
		_, sawCaller = people.Caller(ctx)
		return "ok", nil
	}))
	exec := toolexecution.NewExecutor(stubPolicy{decision: &platform.PolicyDecision{Allowed: true}}, reg, "implement")
	ctx := people.WithCaller(context.Background(), people.Person{ID: "person-1", Role: api.PersonRoleOwner})
	if _, ok := people.Caller(ctx); !ok {
		t.Fatal("fixture request carries no caller")
	}
	_, err := exec.Invoke(ctx, "read", map[string]any{"path": "foo.go"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-caller", Agent: "implement"},
	})
	testutil.FailErr(t, "invoke", err)
	if !invoked || sawCaller {
		t.Fatalf("owner invoked=%v saw the request caller=%v", invoked, sawCaller)
	}
}
