package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type captureCompleteProvider struct {
	stubCuratorProvider
	lastReq modelcall.CompletionRequest
}

func (c *captureCompleteProvider) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.lastReq = req
	return c.stubCuratorProvider.Complete(context.Background(), req)
}

func TestRegistrySummarizerCurate_usesSystemUserMessages(t *testing.T) {
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &captureCompleteProvider{
		stubCuratorProvider: stubCuratorProvider{
			id:        "lite",
			responses: []string{`{"selections":[],"gloss":[]}`},
		},
	}
	cur := newTestRegistrySummarizer(t, provider)
	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID:       "sess-1",
		Agent:           "explore_readonly",
		ParentSessionID: "parent-1",
	})
	_, err := cur.Curate(ctx, snapshot, CurationFocus{Tool: "read", View: "outline", Target: "pkg/a.go"}, 3)
	testutil.FailErr(t, "Curate", err)
	if len(provider.lastReq.Messages) != 2 {
		t.Fatalf("messages = %d want system+user", len(provider.lastReq.Messages))
	}
	if provider.lastReq.Messages[0].Role != api.MessageRoleSystem {
		t.Fatalf("first role = %q", provider.lastReq.Messages[0].Role)
	}
	if !transcript.HasAuthorityNotice(provider.lastReq.Messages[0]) || provider.lastReq.Messages[1].Role != api.MessageRoleUser {
		t.Fatalf("provenance projection = %+v", provider.lastReq.Messages)
	}
	if provider.lastReq.Think != modelcall.ThinkOff {
		t.Fatalf("Think = %v want ThinkOff", provider.lastReq.Think)
	}
	// The request is self-attributing: session identity and purpose ride the
	// provider capture row.
	debug := provider.lastReq.Debug
	if debug.SessionID != "sess-1" || debug.AgentType != "explore_readonly" || debug.ParentSessionID != "parent-1" {
		t.Fatalf("debug attribution = %+v", debug)
	}
	if debug.Purpose != "curate" {
		t.Fatalf("purpose = %q want curate", debug.Purpose)
	}
	rf := provider.lastReq.ResponseFormat
	if rf == nil || rf.Type != modelcall.ResponseFormatJSONSchema || rf.Name != "curate" {
		t.Fatalf("ResponseFormat = %+v want curate json_schema", rf)
	}
}

func TestCurationTaskHintFromContext(t *testing.T) {
	ctx := curationctx.WithTaskHint(context.Background(), "audit altitude")
	if got := curationctx.TaskHint(ctx); got != "audit altitude" {
		t.Fatalf("hint = %q", got)
	}
	f := SearchFocus(ctx, "find", "*.go")
	if !strings.Contains(f.PromptFocus(), "audit altitude") {
		t.Fatalf("prompt focus = %q", f.PromptFocus())
	}
}
