package nativegen

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStreamCappedEnvelopeCannotFinishAsSuccess(t *testing.T) {
	for _, fault := range []string{"capped_channel", "capped_tool"} {
		t.Run(fault, func(t *testing.T) {
			t.Setenv("PW_NATIVEGEN_RESPONSE_FAULT", fault)
			provider, _ := fixtureProvider(t)
			req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Fixture"}}, Tools: []tools.ToolMeta{{Name: "read"}}, MaxTokens: 1}
			completion, err := provider.Complete(t.Context(), req)
			var truncated *failure.ProviderOutputTruncatedError
			if !errors.As(err, &truncated) || truncated.CompletionTokens != 1 {
				t.Fatalf("cap = %+v, %v", completion, err)
			}
			if completion != nil && len(completion.ToolCalls) != 0 {
				t.Fatal("capped header produced calls")
			}
		})
	}
}

func TestStreamRejectsOrphanControlWithTypedProtocolFacts(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_RESPONSE_FAULT", "orphan_channel")
	provider, _ := fixtureProvider(t)
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Fixture"}}, Tools: []tools.ToolMeta{{Name: "read"}}, MaxTokens: 2}
	completion, err := provider.Complete(t.Context(), req)
	var protocol *failure.ProviderOutputProtocolError
	var diagnostic *OutputProtocolError
	if !errors.As(err, &protocol) || !errors.As(err, &diagnostic) || protocol.Kind != failure.MalformedEnvelope || protocol.ProviderID != "fixture" || protocol.Model != DefaultModelID || diagnostic.Controls[0].ID != 101 {
		t.Fatalf("failure = %+v, %v", protocol, err)
	}
	if completion != nil && len(completion.ToolCalls) != 0 {
		t.Fatal("rejected output produced calls")
	}
}

func TestStreamRefusesOldNativeProtocolBeforeBegin(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_OLD_PROTOCOL", "1")
	provider, runtime := fixtureProvider(t)
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Fixture"}}, MaxTokens: 2}
	if _, err := provider.Stream(t.Context(), req); err == nil {
		t.Fatal("accepted protocol version 1")
	}
	if runtime.loaded != nil || runtime.config.HostMemory.Snapshot().ReservedBytes != 0 {
		t.Fatal("old protocol retained model")
	}
}

func TestRejectedOrCappedEnvelopeDiscardsBufferedCalls(t *testing.T) {
	for _, fault := range []string{"buffered_then_orphan", "buffered_then_cap"} {
		t.Run(fault, func(t *testing.T) {
			t.Setenv("PW_NATIVEGEN_RESPONSE_FAULT", fault)
			provider, _ := fixtureProvider(t)
			req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Fixture"}}, Tools: []tools.ToolMeta{{Name: "read"}}, MaxTokens: 4}
			stream, err := provider.Stream(t.Context(), req)
			if err != nil {
				t.Fatalf("start stream: %v", err)
			}
			terminal := false
			for chunk := range stream {
				if len(chunk.ToolCalls) != 0 {
					t.Fatal("failed envelope released buffered calls")
				}
				if chunk.Done {
					terminal = chunk.Err != nil
				}
			}
			if !terminal {
				t.Fatal("missing terminal failure")
			}
		})
	}
}
