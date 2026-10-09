package security

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type providerFaultCase struct {
	name string
	// marker keys the scripted scenario; also the prompt text prefix.
	marker string
	fault  error
	// classified reports whether err is the specific failure this case injected.
	classified func(error) bool
}

func providerFaultCases() []providerFaultCase {
	return []providerFaultCase{
		{
			name:   "rate_limited",
			marker: "fault_429",
			fault: &failure.ProviderRateLimitedError{
				ProviderID: "mock", Model: "mock-1", Status: 429, Detail: "slow down",
			},
			classified: func(err error) bool {
				_, ok := failure.AsProviderRateLimited(err)
				return ok
			},
		},
		{
			name:   "overloaded",
			marker: "fault_503",
			fault: &failure.ProviderOverloadedError{
				ProviderID: "mock", Model: "mock-1", Status: 503, Detail: "engine overloaded",
			},
			classified: func(err error) bool {
				_, ok := failure.AsProviderOverloaded(err)
				return ok
			},
		},
		{
			name:   "context_too_small",
			marker: "fault_ctx",
			fault: &failure.ProviderContextTooSmallError{
				ProviderID: "mock", Model: "mock-1", PromptTokens: 200_000, MaxContext: 8_192,
			},
			classified: func(err error) bool {
				_, ok := failure.AsProviderContextTooSmall(err)
				return ok
			},
		},
		{
			name:   "empty_completion",
			marker: "fault_empty",
			fault: &failure.ProviderEmptyCompletionError{
				ProviderID: "mock", Model: "mock-1",
			},
			classified: func(err error) bool {
				_, ok := failure.AsProviderEmptyCompletion(err)
				return ok
			},
		},
		{
			name:   "unreachable",
			marker: "fault_net",
			// Transport classification follows the wrapped network error.
			fault: &net.OpError{
				Op: "dial", Net: "tcp",
				Err: os.ErrDeadlineExceeded,
			},
			classified: httpclient.Unreachable,
		},
	}
}

// The provider fails after a completed tool round, leaving durable work to recover.
func TestProviderFailureMidRunIsRecoverable(t *testing.T) {
	for _, tc := range providerFaultCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Distinct markers select the failing prompt and its recovery independently.
			script := newScriptedLLM().
				on(tc.marker,
					toolStep("Reading the tree.",
						call("f1", "update_progress", map[string]any{
							"content": "## Progress\n- [>] mid-run fault rehearsal\n",
						})),
					errStep(tc.fault),
				).
				on(tc.marker+"_after", textStep("Recovered and finished."))

			h := wiring.BuildForTest(t,
				wiring.WithLLMClient(script),
				wiring.WithoutCoordinatorLoop(),
			)
			ctx := context.Background()
			dir := h.ProjectDir(t, "fault-"+tc.name)
			sess := createSessionHTTP(t, h.Server, dir)

			_, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID,
				"[[scn:"+tc.marker+"]] do some work then hit the fault")
			if err == nil {
				t.Fatal("prompt succeeded despite an injected provider failure")
			}
			if !tc.classified(err) {
				t.Fatalf("provider failure lost its classification: %v (%T)", err, err)
			}

			assertProgressToolRoundSurvived(t, h, sess.ID)

			waitSessionIdle(t, h, sess.ID, 10*time.Second)

			if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID,
				"[[scn:"+tc.marker+"_after]] try again"); err != nil {
				t.Fatalf("session unusable after a recoverable provider failure: %v", err)
			}
			waitTranscriptContainsHTTP(t, h.Server, sess.ID, "Recovered and finished.", 10*time.Second)
		})
	}
}

func waitSessionIdle(t *testing.T, h *wiring.Harness, sessionID string, timeout time.Duration) {
	t.Helper()
	var last wire.SessionStatus
	ok := testutil.WaitForNoFatal(timeout, func() bool {
		last = getSessionHTTP(t, h.Server, sessionID).Status
		return last == wire.SessionStatusIdle
	})
	if !ok {
		t.Fatalf("session status = %q after provider failure, want idle — the failed "+
			"turn never released the session", last)
	}
}

// Recovery retains both the tool call and its result.
func assertProgressToolRoundSurvived(t *testing.T, h *wiring.Harness, sessionID string) {
	t.Helper()
	msgs, err := h.Store.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages", err)

	callID := ""
	for _, m := range msgs {
		if m.Role != wire.MessageRoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Name == "update_progress" {
				callID = tc.ID
			}
		}
	}
	if callID == "" {
		t.Fatalf("assistant tool-call row from before the failure is gone (%s) — "+
			"a transient provider error discarded finished work", transcriptRoles(msgs))
	}

	for _, m := range msgs {
		if m.Role == wire.MessageRoleTool && m.ToolResult != nil && m.ToolResult.ToolCallID == callID {
			return
		}
	}
	t.Fatalf("tool result for call %s is missing (%s) — the completed round was "+
		"rolled back by the later provider failure", callID, transcriptRoles(msgs))
}

func transcriptRoles(msgs []wire.Message) string {
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, string(m.Role))
	}
	return "roles: " + strings.Join(roles, ",")
}

func TestProviderFaultClassifiersAreDistinct(t *testing.T) {
	for _, tc := range providerFaultCases() {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.classified(tc.fault) {
				t.Fatalf("%s does not classify its own fault %v", tc.name, tc.fault)
			}
			// Provider errors are wrapped in turn context.
			wrapped := errors.Join(errors.New("turn 3"), tc.fault)
			if !tc.classified(wrapped) {
				t.Fatalf("%s classifier does not survive wrapping", tc.name)
			}
			if tc.classified(errors.New("turn 3: " + tc.fault.Error())) {
				t.Fatalf("%s classifier accepts diagnostic text without a typed cause", tc.name)
			}
			for _, other := range providerFaultCases() {
				if other.name == tc.name {
					continue
				}
				if tc.classified(other.fault) {
					t.Fatalf("%s classifier also matches %s — the notice rail cannot "+
						"tell these apart", tc.name, other.name)
				}
			}
		})
	}
}
