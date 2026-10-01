package promptloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// withholdingLLMClient simulates declined secret approvals before replying.
type withholdingLLMClient struct {
	refusals int
	calls    int
	requests []modelcall.CompletionRequest
	reply    string
}

func (c *withholdingLLMClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.calls++
	c.requests = append(c.requests, req)
	if c.calls <= c.refusals {
		return nil, &llm.ModelRequestSecretWithheldError{Guidance: "use the staging key"}
	}
	return &modelcall.Completion{Content: c.reply}, nil
}

func (c *withholdingLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, Done: true}
	close(ch)
	return ch, nil
}

func withheldNudgeDeps(st *store.Memory, client modelcall.LLMClient) promptloop.PromptLoopDeps {
	deps := promptloop.StoreDeps(st)
	deps.LLM = client
	deps.Policy = &recordingToolPolicy{}
	deps.SecretWithheldNudge = func(_ context.Context, _ *api.Session, guidance string) promptloop.HostNudge {
		return promptloop.HostNudge{Content: "host: request not sent, user declined. guidance=" + guidance}
	}
	return deps
}

func TestSecretWithheldTurnTellsModelAndContinues(t *testing.T) {
	st := store.NewMemory()
	client := &withholdingLLMClient{refusals: 1, reply: "carried on without the key"}
	loop := promptloop.NewPromptLoopForTest(withheldNudgeDeps(st, client))
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run must not fail on a declined credential", err)
	if result.LastAssistantContent != "carried on without the key" {
		t.Fatalf("content = %q want the turn taken after the refusal", result.LastAssistantContent)
	}
	if client.calls != 2 {
		t.Fatalf("provider calls = %d want 2 (refused, then retaken)", client.calls)
	}

	// The retry includes the refusal and user guidance.
	var sawNudge bool
	for _, msg := range client.requests[1].Messages {
		if strings.Contains(msg.Content, "user declined") && strings.Contains(msg.Content, "guidance=use the staging key") {
			sawNudge = true
		}
	}
	if !sawNudge {
		t.Fatalf("retry request carried no refusal notice: %+v", client.requests[1].Messages)
	}

	msgs, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "user declined") && msg.Visibility != api.MessageVisibilityInternal {
			t.Fatalf("refusal notice must stay internal, got visibility %q", msg.Visibility)
		}
	}
}

// Retained user text can trigger the same secret approval on each retry.
func TestSecretWithheldRepeatedRefusalEndsRunWithoutError(t *testing.T) {
	st := store.NewMemory()
	client := &withholdingLLMClient{refusals: 5, reply: "unreachable"}
	loop := promptloop.NewPromptLoopForTest(withheldNudgeDeps(st, client))
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "coordinator",
	})
	testutil.FailErr(t, "a repeated refusal must not fail the run", err)
	if result == nil {
		t.Fatal("result = nil want an empty run result")
	}
	if result.LastAssistantID != "" {
		t.Fatalf("last assistant = %q want none: no turn ever reached a provider", result.LastAssistantID)
	}
	if client.calls != 2 {
		t.Fatalf("provider calls = %d want 2 (one retry, then stop asking)", client.calls)
	}
}
