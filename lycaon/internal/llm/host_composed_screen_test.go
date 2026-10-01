package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The screen reports composition as a fact and acts on none of it.
func TestModelSecretScreenReportsRequestComposition(t *testing.T) {
	for _, tc := range []struct {
		name        string
		composition modelcall.RequestComposition
		want        bool
	}{
		{name: "conversation", composition: modelcall.CompositionConversation, want: false},
		{name: "host utility", composition: modelcall.CompositionHostUtility, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var finding secretmatch.Alert
			screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
				finding = got
				return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
			})
			req := modelSecretRequest()
			req.Composition = tc.composition
			_, err := screen.Screen(context.Background(), ScreenDestination{ID: "fireworks-main"}, req)
			testutil.FailErr(t, "screen request", err)
			if finding.HostComposed != tc.want {
				t.Fatalf("alert HostComposed = %v, want %v", finding.HostComposed, tc.want)
			}
		})
	}
}

// Utility calls are stamped host-composed at their shared funnel, so a new one
// never inherits the card.
func TestUtilityCallsAreStampedHostComposed(t *testing.T) {
	recorder := &requestRecorder{namedStubProvider: namedStubProvider{id: "lite", content: "summary"}}
	r := newFallbackTestSummarizer(t, recorder, recorder)

	_, err := r.Summarize(context.Background(), "sys", "user prompt", 64)
	testutil.FailErr(t, "Summarize", err)
	if recorder.last.Composition != modelcall.CompositionHostUtility {
		t.Fatalf("composition = %q, want host_utility", recorder.last.Composition)
	}
}

// A utility call carries the session it runs for even when the call site passes
// no identity of its own.
func TestUtilityCallsCarryAmbientSessionAttribution(t *testing.T) {
	recorder := &requestRecorder{namedStubProvider: namedStubProvider{id: "lite", content: "summary"}}
	r := newFallbackTestSummarizer(t, recorder, recorder)
	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID: "worker-1", Agent: "implement", ParentSessionID: "chat-1",
		ProjectID: "proj-1", ProjectDir: "/tmp/proj",
	})

	_, err := r.Summarize(ctx, "sys", "user prompt", 64)
	testutil.FailErr(t, "Summarize", err)
	got := recorder.last.Debug
	if got.SessionID != "worker-1" || got.ParentSessionID != "chat-1" ||
		got.ProjectID != "proj-1" || got.ProjectDir != "/tmp/proj" || got.AgentType != "implement" {
		t.Fatalf("attribution = %+v, want the compacting session's identity", got)
	}
}

// Worker compaction uses the parent chat’s screening grants.
func TestCompactionScreensAgainstTheChatItRunsUnder(t *testing.T) {
	var finding secretmatch.Alert
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID: "worker-1", Agent: "implement", ParentSessionID: "chat-1", ProjectID: "proj-1",
	})
	req := modelSecretRequest()
	req.Debug = stampSessionAttribution(modelcall.RequestDebug{Purpose: "compaction"}, curationctx.SessionFrom(ctx))

	_, err := screen.Screen(ctx, ScreenDestination{ID: "fireworks-main"}, req)
	testutil.FailErr(t, "screen a worker compaction", err)
	if finding.SessionID != "worker-1" || finding.RootSessionID != "chat-1" {
		t.Fatalf("alert session = %q root = %q, want worker-1 under chat-1", finding.SessionID, finding.RootSessionID)
	}
}

// A held send is not retried elsewhere: no second destination, no local
// truncation standing in for it.
func TestSummarizeDoesNotFallBackOnAHeldSend(t *testing.T) {
	held := &ModelRequestSecretWithheldError{Guidance: "keep it local"}
	lite := &namedStubProvider{id: "lite", err: held}
	coord := &namedStubProvider{id: "coord", content: "coordinator summary"}
	r := newFallbackTestSummarizer(t, lite, coord)

	got, err := r.Summarize(context.Background(), "sys", "user prompt with a secret", 64)
	if !errors.Is(err, ErrModelRequestSecretWithheld) {
		t.Fatalf("err = %v, want the withheld send", err)
	}
	if got != "" {
		t.Fatalf("summary = %q, want nothing to stand in for a held send", got)
	}
	if coord.calls != 0 {
		t.Fatalf("coordinator calls = %d, want the same bytes not offered to a second destination", coord.calls)
	}
}

// A screen the host could not run is a fault, not a summary: the truncating
// fallback would return a slice of the unscreened prompt.
func TestSummarizeDoesNotTruncateAroundAScreenFault(t *testing.T) {
	fault := &ModelRequestSecretScreenFaultError{Stage: secretmatch.FaultStageCheckpointsUnwired}
	lite := &namedStubProvider{id: "lite", err: fault}
	coord := &namedStubProvider{id: "coord", err: fault}
	r := newFallbackTestSummarizer(t, lite, coord)

	got, err := r.Summarize(context.Background(), "sys", "prompt body "+modelScreenGitHubToken, 64)
	if !errors.Is(err, ErrModelRequestSecretScreenFailed) {
		t.Fatalf("err = %v, want the screen fault", err)
	}
	if strings.Contains(got, modelScreenGitHubToken) {
		t.Fatal("truncate fallback handed back the prompt the screen could not clear")
	}
}

// requestRecorder keeps the last request a provider was handed.
type requestRecorder struct {
	namedStubProvider
	last modelcall.CompletionRequest
}

func (p *requestRecorder) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.last = req
	return p.namedStubProvider.Complete(ctx, req)
}
