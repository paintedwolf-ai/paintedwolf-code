package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestListForPromptMarksDeferredMetas asserts the prompt tool policy flags
// implement-profile deferred tools so the prompt loop can withhold their schemas.
func TestListForPromptMarksDeferredMetas(t *testing.T) {
	h := BuildForTest(t, WithDecider(decide.Absent{}))
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)

	policy := h.SessionMgr.Guards.Policy()
	metas := policy.ListForPrompt(ctx, sess, "implement")
	deferred := map[string]bool{}
	var hasRequestTools bool
	for _, meta := range metas {
		if meta.Deferred {
			deferred[meta.Name] = true
		}
		if meta.Name == "request_tools" {
			hasRequestTools = true
			if meta.Deferred {
				t.Fatal("request_tools must never be deferred")
			}
		}
	}
	for _, name := range []string{"copy", "move", "scan_query", "source_history", "page_open", "page_close", "http_request", "secret_generate", "handoff_reserve"} {
		if !deferred[name] {
			t.Fatalf("%s must be marked deferred for implement; deferred set = %v", name, deferred)
		}
	}
	if deferred["read"] || deferred["write"] {
		t.Fatalf("hot tools must not be deferred: %v", deferred)
	}
	if !hasRequestTools {
		t.Fatal("request_tools missing from implement prompt surface")
	}
}
