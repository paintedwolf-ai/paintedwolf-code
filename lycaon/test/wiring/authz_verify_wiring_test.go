package wiring

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func authzVerifyMockClient() *llm.MockProvider {
	return llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "Done."}}})
}

// The built app seals an authorization context for each prompt it runs.
func TestBuildSessionWiresAuthzCapturer(t *testing.T) {
	h := BuildForTest(t, WithLLMClient(authzVerifyMockClient()))
	if !h.Sessions.Manager.Runner.Authorization.Wired() {
		t.Fatal("built app left the authorization sealer unwired")
	}
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, h.ProjectDir(t, "authz-seal"))
	testutil.FailErr(t, "create session", err)
	contexts, err := authzcontext.NewSQLStore(h.DB).ListContexts(t.Context(), sess.ID)
	testutil.FailErr(t, "list contexts before prompt", err)
	if len(contexts) != 0 {
		t.Fatalf("contexts before any prompt = %d", len(contexts))
	}
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "hello")
	testutil.FailErr(t, "prompt", err)
	contexts, err = authzcontext.NewSQLStore(h.DB).ListContexts(t.Context(), sess.ID)
	testutil.FailErr(t, "list contexts after prompt", err)
	if len(contexts) != 1 || contexts[0].SessionID != sess.ID {
		t.Fatalf("sealed contexts after one prompt = %+v", contexts)
	}
}

// A seal failure refuses the turn and queues the authz.seal_failed inform.
func TestAuthzSealFailureSurfacesViaNudge(t *testing.T) {
	h := BuildForTest(t, WithLLMClient(authzVerifyMockClient()))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, h.ProjectDir(t, "authz-seal-fail"))
	testutil.FailErr(t, "create session", err)
	want := anchor.InformRender(anchor.AuthzSealFailed)
	if want == "" {
		t.Fatal("anchor registry has no authz.seal_failed inform")
	}
	if pending := h.Sessions.Manager.Coordinator.Guidance.PendingIDs(t.Context(), sess.ID); slices.Contains(pending, want) {
		t.Fatalf("seal failure queued before any prompt: %v", pending)
	}
	// A sealer without a store fails every seal.
	h.Sessions.Manager.Runner.Authorization.SetSealer(&authzcontext.Sealer{})
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "hello")
	if !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("prompt with failing sealer err = %v, want ErrSealFailed", err)
	}
	if pending := h.Sessions.Manager.Coordinator.Guidance.PendingIDs(t.Context(), sess.ID); !slices.Contains(pending, want) {
		t.Fatalf("pending guidance = %v, want %q", pending, want)
	}
}
