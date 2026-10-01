package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestTaskWithoutDeadlineStillCancels(t *testing.T) {
	ctx, cancel := taskContext(t.Context(), 0)
	if _, bounded := ctx.Deadline(); bounded {
		t.Fatal("model work has a task deadline")
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("unbounded task ignored cancellation")
	}
}

func TestPromptTransportReplaysSameOperationAfterLostReply(t *testing.T) {
	var requests atomic.Int32
	operations := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var prompt map[string]string
		testutil.FailErr(t, "decode retried prompt", json.NewDecoder(r.Body).Decode(&prompt))
		operations[prompt["operation_id"]]++
		if requests.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			testutil.FailErr(t, "drop accepted reply", err)
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"queued","operation_id":"fixed","message_id":"fixed","session_revision":0}`)
	}))
	defer server.Close()
	client := &liveClient{http: server.Client()}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, bytes.NewBufferString(`{"operation_id":"fixed"}`))
	testutil.FailErr(t, "make idempotent request", err)
	resp, err := client.request(req, true)
	testutil.FailErr(t, "recover accepted prompt", err)
	var accepted wire.PromptAcceptedResponse
	testutil.FailErr(t, "decode replay admission", json.NewDecoder(resp.Body).Decode(&accepted))
	_ = resp.Body.Close()
	if accepted.Status != "queued" || accepted.OperationID != "fixed" || accepted.MessageID != "fixed" {
		t.Fatalf("replayed admission=%+v", accepted)
	}
	if requests.Load() != 2 || len(operations) != 1 || operations["fixed"] != 2 {
		t.Fatal("request replay changed operation identity")
	}
}

func TestMutationAndPermanentErrorsAreNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		var count atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { count.Add(1); w.WriteHeader(code) }))
		client := &liveClient{http: server.Client()}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
		testutil.FailErr(t, "make mutation", err)
		response, err := client.do(req)
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil {
			t.Fatal("accepted failed mutation")
		}
		if count.Load() != 1 {
			t.Fatal("replayed mutation without an operation identity")
		}
		server.Close()
	}
	if retryableLiveError(&liveHTTPError{Status: 401}) {
		t.Fatal("authentication failure was transient")
	}
}

func TestPollingRecoversFromTruncatedBody(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if count.Add(1) == 1 {
			w.Header().Set("Content-Length", "50")
			_, _ = io.WriteString(w, "{")
			return
		}
		_, _ = io.WriteString(w, `{"id":"s"}`)
	}))
	defer server.Close()
	client := &liveClient{base: server.URL, http: server.Client()}
	sess, err := client.getSession(t.Context(), "s")
	testutil.FailErr(t, "read full retry response", err)
	if sess.ID != "s" || count.Load() != 2 {
		t.Fatal("partial reply lost the session")
	}
}

func TestRetryAfterAndCancellation(t *testing.T) {
	if got := liveRetryDelay(0, &http.Response{Header: http.Header{"Retry-After": []string{"60"}}}); got != time.Minute {
		t.Fatalf("retry-after=%v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { cancel(); w.WriteHeader(503) }))
	defer server.Close()
	client := &liveClient{base: server.URL, http: server.Client()}
	if _, err := client.getSession(ctx, "s"); err == nil {
		t.Fatal("canceled poll succeeded")
	}
}

func TestLiveRequestsWaitForExplicitCompletionOrCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelRequest), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				close(entered)
				select {
				case <-release:
					_, _ = io.WriteString(w, `{"ready":true}`)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			defer close(release)
			client := newLiveClient(server.URL, "fixture")
			// Verify the actual context delivered by http.Client, including any client deadline.
			client.http.Transport = deadlineInspectingTransport{t, server.Client().Transport}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
			testutil.FailErr(t, "create preparation request", err)
			finished := make(chan error, 1)
			go func() {
				response, requestErr := client.do(request)
				if response != nil {
					_ = response.Body.Close()
				}
				finished <- requestErr
			}()
			<-entered
			if cancelRequest {
				cancel()
			} else {
				release <- struct{}{}
			}
			err = <-finished
			if cancelRequest {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation returned %v", err)
				}
			} else {
				testutil.FailErr(t, "finish preparation", err)
			}
			if calls.Load() != 1 {
				t.Fatal("preparation mutation was replayed")
			}
		})
	}
}

type deadlineInspectingTransport struct {
	t    *testing.T
	next http.RoundTripper
}

func (transport deadlineInspectingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if _, exists := request.Context().Deadline(); exists {
		transport.t.Error("live HTTP request introduced a deadline")
	}
	return transport.next.RoundTrip(request)
}

func TestEvidenceCollectionSharesCancellationUntilShutdown(t *testing.T) {
	parent, stop := context.WithCancel(t.Context())
	ctx, done := evidenceContext(parent)
	defer done()
	if _, bounded := ctx.Deadline(); bounded {
		t.Fatal("normal evidence collection has a deadline")
	}
	stop()
	if ctx.Err() != context.Canceled {
		t.Fatal("evidence collection ignored operator cancellation")
	}
	cleanup, finish := evidenceContext(parent)
	defer finish()
	if cleanup.Err() != nil {
		t.Fatal("canceled run has no opportunity to preserve evidence")
	}
	if _, bounded := cleanup.Deadline(); !bounded {
		t.Fatal("shutdown cleanup can prevent cancellation indefinitely")
	}
}

func TestOnlyPreparationTransportFailuresPermitWholeAttemptRetry(t *testing.T) {
	for _, beforeCandidate := range []bool{true, false} {
		t.Run(fmt.Sprintf("preparation=%t", beforeCandidate), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/projects":
					if beforeCandidate {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = io.WriteString(w, `{"id":"project"}`)
				case "/v1/sessions/s/prompts":
					w.WriteHeader(http.StatusAccepted)
					_, _ = io.WriteString(w, `{"status":"queued","operation_id":"submission","message_id":"submission","session_revision":0}`)
				case "/v1/sessions/s/messages":
					_, _ = io.WriteString(w, `{"messages":[]}`)
				default:
					_, _ = io.WriteString(w, `{"id":"s","status":"idle"}`)
				}
			}))
			defer server.Close()
			client := newLiveClient(server.URL, "fixture")
			client.observeFailure = func(context.Context, string) error { return io.EOF }
			opts := SuiteOptions{LiveOptions: LiveOptions{CaptureDir: t.TempDir()}, Suite: &Suite{root: t.TempDir()}}
			result := runSuiteCase(t.Context(), client, opts,
				SuiteCase{CorpusTask: CorpusTask{ID: "case", Prompt: "implement"}, Project: "."},
				t.TempDir(), 1, func(CaseReport) error { return nil })
			if result.Status != "error" {
				t.Fatalf("lost transport failure: %+v", result)
			}
			if beforeCandidate {
				if result.Failure == nil || *result.Failure != (ExecutionFailure{Kind: "harness", Code: "preparation_transport", Retryable: true}) {
					t.Fatalf("preparation failure not recoverable: %+v", result)
				}
			} else if result.Failure == nil || *result.Failure != (ExecutionFailure{Kind: "harness", Code: "execution_collection"}) {
				t.Fatalf("candidate transport failure must remain typed without replay: %+v", result.Failure)
			}
		})
	}
}
