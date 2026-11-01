package nativegen

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNativeProcessResponseConformance(t *testing.T) {
	for _, mode := range []string{"before_output", "after_output", "empty_eos"} {
		for _, complete := range []bool{false, true} {
			name := mode + "/stream"
			if complete {
				name = mode + "/complete"
			}
			t.Run(name, func(t *testing.T) {
				provider, runtime := fixtureProvider(t)
				t.Setenv("PW_NATIVEGEN_RESPONSE_FAULT", mode)
				req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "ping"}}, MaxTokens: 8}
				var terminal error
				if complete {
					_, terminal = provider.Complete(t.Context(), req)
				} else {
					stream, err := provider.Stream(t.Context(), req)
					if err != nil {
						t.Fatalf("open native stream: %v", err)
					}
					done := 0
					for chunk := range stream {
						if chunk.Done {
							done++
							terminal = chunk.Err
						}
					}
					if done != 1 {
						t.Fatalf("terminal chunks = %d", done)
					}
				}
				if mode == "empty_eos" {
					var empty *failure.ProviderEmptyCompletionError
					if !errors.As(terminal, &empty) || empty.ProviderID != provider.ID() || empty.Model != DefaultModelID {
						t.Fatalf("empty response classification: %v", terminal)
					}
				} else if interrupted, ok := failure.AsProviderResponseInterrupted(terminal); !ok || interrupted.ProviderID != provider.ID() || interrupted.Model != DefaultModelID {
					t.Fatalf("interrupted response classification: %v", terminal)
				}
				if err := runtime.Close(); err != nil {
					t.Fatalf("close runtime: %v", err)
				}
				if runtime.config.HostMemory.Snapshot().ReservedBytes != 0 || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 0 {
					t.Fatal("terminal response retained memory after close")
				}
			})
		}
	}
}
