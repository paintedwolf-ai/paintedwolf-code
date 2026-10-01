package openaicompat

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type interruptedResponseReader struct{ err error }

func (r interruptedResponseReader) Read([]byte) (int, error) { return 0, r.err }

func TestOpenAIInterruptedResponsePreservesPartialOutputAndCause(t *testing.T) {
	transportErr := &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}
	for _, cause := range []error{transportErr, io.EOF} {
		t.Run(cause.Error(), func(t *testing.T) {
			body := io.MultiReader(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"Partial response\"}}]}\n\n"), interruptedResponseReader{cause})
			provider := &Provider{id: "fixture"}
			stream, err := provider.streamChatCompletionFromResponse(t.Context(), modelcall.CompletionRequest{Model: "model"}, &http.Response{Body: io.NopCloser(body)})
			testutil.FailErr(t, "open interrupted response", err)
			var content string
			var terminal error
			for chunk := range stream {
				content += chunk.Content
				if chunk.Err != nil {
					if !chunk.Done {
						t.Fatal("interrupted response did not terminate the stream")
					}
					terminal = chunk.Err
				}
			}
			response, ok := failure.AsProviderResponseInterrupted(terminal)
			if !ok || response.ProviderID != "fixture" || response.Model != "model" || content != "Partial response" {
				t.Fatalf("lost interrupted response evidence: content=%q error=%v", content, terminal)
			}
			code, coded := noticeerr.CodeOf(terminal)
			if !coded || code != wire.NoticeCodeProviderResponseInterrupted {
				t.Fatalf("missing durable response failure code: %q", code)
			}
			want := cause
			if errors.Is(cause, io.EOF) {
				want = io.ErrUnexpectedEOF
			}
			if !errors.Is(terminal, want) {
				t.Fatalf("lost transport cause: %v", terminal)
			}
		})
	}
}

func TestOpenAIEmptyStreamRetainsWireTerminalFact(t *testing.T) {
	for _, test := range []struct {
		name, body string
		terminal   bool
	}{
		{"terminal", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true},
		{"interrupted", "data: {\"choices\":[{\"delta\":{}}]}\n\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &Provider{id: "fixture"}
			stream, err := provider.streamChatCompletionFromResponse(t.Context(), modelcall.CompletionRequest{Model: "model"}, &http.Response{Body: io.NopCloser(strings.NewReader(test.body))})
			testutil.FailErr(t, "read empty stream", err)
			_, _, err = modelcall.CollectStream(stream)
			empty, ok := failure.AsProviderEmptyCompletion(err)
			if !ok || empty.Terminal != test.terminal {
				t.Fatalf("terminal=%v error=%+v", test.terminal, err)
			}
		})
	}
}
