package llmtest

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecordingClientPreservesDelegationAndAttemptOrder(t *testing.T) {
	inner := NewKickDrivenMock(KickDrivenConfig{Fallback: func(_ context.Context, snapshot Snapshot) modelcall.Completion {
		return modelcall.Completion{Content: snapshot.LastUserMessage}
	}})
	recorder := NewRecordingClient(inner)
	request := func(text string) modelcall.CompletionRequest {
		return modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: text}}}
	}
	result, err := recorder.Complete(t.Context(), request("first"))
	testutil.FailErr(t, "complete recorded request", err)
	if result.Content != "first" {
		t.Fatalf("completion=%+v", result)
	}
	stream, err := recorder.Stream(t.Context(), request("second"))
	testutil.FailErr(t, "stream recorded request", err)
	var chunks []modelcall.StreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 1 || chunks[0].Content != "second" || !chunks[0].Done {
		t.Fatalf("stream=%+v", chunks)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := recorder.Complete(ctx, request("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled completion=%v", err)
	}
	history := recorder.AllRequests()
	if len(history) != 3 {
		t.Fatalf("recorded attempts=%d", len(history))
	}
	for i, want := range []string{"first", "second", "cancelled"} {
		if history[i].Messages[0].Content != want {
			t.Fatalf("request %d=%+v", i, history[i])
		}
	}
	history[0] = modelcall.CompletionRequest{}
	if recorder.AllRequests()[0].Messages[0].Content != "first" {
		t.Fatal("caller replaced recorded history")
	}
	if recorder.LastRequest().Messages[0].Content != "cancelled" {
		t.Fatal("failed attempt missing from last request")
	}
}

func TestRecordingClientWithoutDelegateCapturesRequests(t *testing.T) {
	recorder := NewRecordingClient(nil)
	result, err := recorder.Complete(t.Context(), modelcall.CompletionRequest{Model: "complete"})
	testutil.FailErr(t, "record completion", err)
	if result == nil || result.Content != "" {
		t.Fatalf("completion=%+v", result)
	}
	stream, err := recorder.Stream(t.Context(), modelcall.CompletionRequest{Model: "stream"})
	testutil.FailErr(t, "record stream", err)
	if _, open := <-stream; open {
		t.Fatal("delegate-free stream stayed open")
	}
	if len(recorder.AllRequests()) != 2 || recorder.LastRequest().Model != "stream" {
		t.Fatal("delegate-free requests were not recorded")
	}
}
