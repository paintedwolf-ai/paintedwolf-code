package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestManualProviderAutoReply(t *testing.T) {
	p := NewManualProvider()
	p.SetAuto(true, "auto says hi")

	got, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	if err != nil {
		testutil.FailErr(t, "complete", err)
	}
	if got.Content != "auto says hi" {
		t.Fatalf("auto reply = %q, want %q", got.Content, "auto says hi")
	}
}

func TestManualProviderInterceptAndRespond(t *testing.T) {
	p := NewManualProvider()
	p.SetAuto(false, "")

	done := make(chan *modelcall.Completion, 1)
	go func() {
		c, err := p.Complete(context.Background(), modelcall.CompletionRequest{
			Model:    "gpt-x",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "plan it"}},
			Debug:    modelcall.RequestDebug{SessionID: "session-1"},
		})
		if err != nil {
			t.Errorf("complete: %v", err)
		}
		done <- c
	}()

	pend, ok := p.Pending(context.Background(), "", 2*time.Second)
	if !ok {
		t.Fatal("expected a pending completion")
	}
	if pend.Model != "gpt-x" || len(pend.Messages) != 1 || pend.SessionID != "session-1" {
		t.Fatalf("pending mismatch: model=%q messages=%d session=%q", pend.Model, len(pend.Messages), pend.SessionID)
	}

	if err := p.RespondWithChunks(pend.ID, "here is the plan", []api.ToolCall{{ID: "t1", Name: "read", Args: map[string]any{"path": "x"}}}, nil); err != nil {
		testutil.FailErr(t, "respond", err)
	}

	select {
	case c := <-done:
		if c.Content != "here is the plan" {
			t.Fatalf("content = %q", c.Content)
		}
		if len(c.ToolCalls) != 1 || c.ToolCalls[0].Name != "read" {
			t.Fatalf("tool calls = %+v", c.ToolCalls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Complete did not return after Respond")
	}
}

func TestManualProviderTimeoutFallsBack(t *testing.T) {
	p := NewManualProvider()
	p.SetAuto(false, "")
	p.timeout = 50 * time.Millisecond

	got, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		testutil.FailErr(t, "complete", err)
	}
	if got.Content == "" {
		t.Fatal("expected a fallback completion on timeout")
	}
}

func TestManualProviderDirtyStreamChunks(t *testing.T) {
	p := NewManualProvider()
	p.SetAuto(false, "")

	done := make(chan []modelcall.StreamChunk, 1)
	go func() {
		ch, err := p.Stream(context.Background(), modelcall.CompletionRequest{
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "split"}},
		})
		if err != nil {
			t.Errorf("stream: %v", err)
			done <- nil
			return
		}
		var got []modelcall.StreamChunk
		for c := range ch {
			got = append(got, c)
		}
		done <- got
	}()

	pend, ok := p.Pending(context.Background(), "", 2*time.Second)
	if !ok {
		t.Fatal("expected a pending completion")
	}
	chunks := []modelcall.StreamChunk{
		{Content: "Hel"},
		{Content: "lo", Done: true},
	}
	if err := p.RespondWithChunks(pend.ID, "", nil, chunks); err != nil {
		testutil.FailErr(t, "respond with chunks", err)
	}

	select {
	case got := <-done:
		if len(got) != 2 {
			t.Fatalf("chunks = %d, want 2: %+v", len(got), got)
		}
		if got[0].Content != "Hel" || got[1].Content != "lo" || !got[1].Done {
			t.Fatalf("unexpected chunks: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not finish after RespondWithChunks")
	}
}

func TestManualProviderStreamsPlainContentByteForByte(t *testing.T) {
	p := NewManualProvider()
	p.SetAuto(false, "")
	const content = "Done.\n\n```json\n{\"cited_evidence\": [{\"path\": \"report.py\", \"line\": 2}]}\n```"
	type result struct {
		chunks []modelcall.StreamChunk
		err    error
	}
	done := make(chan result, 1)
	go func() {
		ch, err := p.Stream(context.Background(), modelcall.CompletionRequest{
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "close out"}},
		})
		if err != nil {
			done <- result{err: err}
			return
		}
		var got []modelcall.StreamChunk
		for c := range ch {
			got = append(got, c)
		}
		done <- result{chunks: got}
	}()
	pend, ok := p.Pending(context.Background(), "", 2*time.Second)
	if !ok {
		t.Fatal("expected a pending completion")
	}
	if err := p.RespondWithChunks(pend.ID, content, nil, nil); err != nil {
		testutil.FailErr(t, "respond", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			testutil.FailErr(t, "stream", r.err)
		}
		var b strings.Builder
		for _, c := range r.chunks {
			b.WriteString(c.Content)
		}
		if b.String() != content || len(r.chunks) < 2 || !r.chunks[len(r.chunks)-1].Done {
			t.Fatalf("streamed %q in %d chunk(s), want the content verbatim across line chunks", b.String(), len(r.chunks))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not finish after Respond")
	}
}

func TestManualProviderPendingFiltersBySession(t *testing.T) {
	p := NewManualProvider()
	p.SetAuto(false, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	complete := func(sessionID string) {
		_, _ = p.Complete(ctx, modelcall.CompletionRequest{Debug: modelcall.RequestDebug{SessionID: sessionID}})
	}
	go complete("stale")
	stale, ok := p.Pending(ctx, "", 2*time.Second)
	if !ok || stale.SessionID != "stale" {
		t.Fatalf("unfiltered pending = %+v, %v; want the stale session's request", stale, ok)
	}
	if _, ok := p.Pending(ctx, "current", 0); ok {
		t.Fatal("filtered pending returned another session's request")
	}
	found := make(chan *ManualPending, 1)
	go func() {
		pend, _ := p.Pending(ctx, "current", 2*time.Second)
		found <- pend
	}()
	go complete("current")
	select {
	case pend := <-found:
		if pend == nil || pend.SessionID != "current" {
			t.Fatalf("filtered pending = %+v; want the current session's request", pend)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("filtered pending did not receive the current session's request")
	}
}

func TestManualProviderAutoReplyReleasesWaitingRequests(t *testing.T) {
	p := NewManualProvider()
	p.SetAuto(false, "")
	done := make(chan *modelcall.Completion, 1)
	go func() {
		c, err := p.Complete(context.Background(), modelcall.CompletionRequest{Debug: modelcall.RequestDebug{SessionID: "s1"}})
		if err != nil {
			t.Errorf("complete: %v", err)
		}
		done <- c
	}()
	if _, ok := p.Pending(context.Background(), "s1", 2*time.Second); !ok {
		t.Fatal("expected a pending completion")
	}
	p.SetAuto(true, "resumed")
	select {
	case c := <-done:
		if c == nil || c.Content != "resumed" {
			t.Fatalf("released completion = %+v; want the auto reply", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting request was not released when auto-reply resumed")
	}
	if _, ok := p.Pending(context.Background(), "", 0); ok {
		t.Fatal("released request is still pending")
	}
}
