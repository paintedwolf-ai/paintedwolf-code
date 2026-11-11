package openaicompat

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
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

func abortResponseMidBody(t *testing.T, w http.ResponseWriter, contentType, partial string) {
	t.Helper()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", "4096")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(partial))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if hijacker, ok := w.(http.Hijacker); ok {
		if conn, _, err := hijacker.Hijack(); err == nil {
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
			_ = conn.Close()
		}
	}
}

func TestOpenAIHTTPServerInterruptedAndEmptyResponses(t *testing.T) {
	req := modelcall.CompletionRequest{
		Model:    "gpt-test",
		Messages: []wire.Message{{Role: wire.MessageRoleUser, Content: "hello"}},
	}

	t.Run("mid-stream connection reset", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			abortResponseMidBody(t, w, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n")
		}))
		defer srv.Close()

		p := New("fireworks", srv.URL, "key", []modelinfo.Entry{{ID: "gpt-test"}})
		ch, err := p.Stream(t.Context(), req)
		testutil.FailErr(t, "open stream", err)
		_, _, err = modelcall.CollectStream(ch)
		interrupted, ok := failure.AsProviderResponseInterrupted(err)
		if !ok || interrupted.ProviderID != "fireworks" || interrupted.Model != "gpt-test" {
			t.Fatalf("err = %v, want ProviderResponseInterruptedError", err)
		}
	})

	t.Run("stream closes after output without terminal event", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n"))
		}))
		defer srv.Close()

		p := New("fireworks", srv.URL, "key", []modelinfo.Entry{{ID: "gpt-test"}})
		ch, err := p.Stream(t.Context(), req)
		testutil.FailErr(t, "open stream", err)
		_, _, err = modelcall.CollectStream(ch)
		interrupted, ok := failure.AsProviderResponseInterrupted(err)
		if !ok || !errors.Is(err, io.ErrUnexpectedEOF) || interrupted.ProviderID != "fireworks" {
			t.Fatalf("err = %v, want ProviderResponseInterruptedError wrapping unexpected EOF", err)
		}
	})

	t.Run("stream closes before any output", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		p := New("fireworks", srv.URL, "key", []modelinfo.Entry{{ID: "gpt-test"}})
		ch, err := p.Stream(t.Context(), req)
		testutil.FailErr(t, "open stream", err)
		_, _, err = modelcall.CollectStream(ch)
		empty, ok := failure.AsProviderEmptyCompletion(err)
		if !ok || !empty.Retryable || empty.ProviderID != "fireworks" {
			t.Fatalf("err = %v, want retryable ProviderEmptyCompletionError", err)
		}
	})

	t.Run("non-streaming complete body breaks after headers", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			abortResponseMidBody(t, w, "application/json", `{"choices":[{"message":{"content":"half`)
		}))
		defer srv.Close()

		p := New("fireworks", srv.URL, "key", []modelinfo.Entry{{ID: "gpt-test"}})
		_, err := p.Complete(t.Context(), req)
		interrupted, ok := failure.AsProviderResponseInterrupted(err)
		if !ok || interrupted.ProviderID != "fireworks" || interrupted.Model != "gpt-test" {
			t.Fatalf("err = %v, want ProviderResponseInterruptedError", err)
		}
	})

	t.Run("non-sse json stream response body breaks after headers", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			abortResponseMidBody(t, w, "application/json", `{"choices":[{"message":{"content":"half`)
		}))
		defer srv.Close()

		p := New("fireworks", srv.URL, "key", []modelinfo.Entry{{ID: "gpt-test"}})
		ch, err := p.Stream(t.Context(), req)
		testutil.FailErr(t, "open json fallback stream", err)
		_, _, err = modelcall.CollectStream(ch)
		interrupted, ok := failure.AsProviderResponseInterrupted(err)
		if !ok || interrupted.ProviderID != "fireworks" || interrupted.Model != "gpt-test" {
			t.Fatalf("err = %v, want ProviderResponseInterruptedError", err)
		}
	})
}
