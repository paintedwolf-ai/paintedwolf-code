package toolusage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// watchSessionFailure subscribes before admission. The response headers are flushed
// only after the hub has registered the subscriber, including for a fast failed turn.
func (c *liveClient) watchSessionFailure(ctx context.Context, sessionID string) (context.Context, context.CancelFunc, error) {
	if c.observeCompletion != nil {
		return ctx, func() {}, nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/events", nil)
	if err != nil {
		cancel(err)
		return ctx, func() {}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	// The ordinary transport buffers whole responses and cannot consume SSE.
	resp, err := c.http.Do(req) //nolint:bodyclose,gosec // G704 — the operator selects the evaluation sidecar; the SSE reader goroutine closes the body.
	if err != nil {
		cancel(err)
		return ctx, func() {}, err
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		_ = resp.Body.Close()
		err := fmt.Errorf("session events require SSE (HTTP %d)", resp.StatusCode)
		cancel(err)
		return ctx, func() {}, err
	}
	go func() {
		defer func() { _ = resp.Body.Close() }()
		err := readSessionFailure(resp.Body, sessionID)
		if ctx.Err() == nil {
			cancel(err)
		}
	}()
	return ctx, func() { cancel(context.Canceled); _ = resp.Body.Close() }, nil
}

func readSessionFailure(reader io.Reader, sessionID string) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		var envelope wire.EventEnvelope
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &envelope); err != nil {
			return fmt.Errorf("decode session event: %w", err)
		}
		if envelope.Topic != wire.EventTopicSession || envelope.Scope.SessionID != sessionID {
			continue
		}
		var event wire.SessionEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			return fmt.Errorf("decode session lifecycle: %w", err)
		}
		if event.ID != sessionID {
			return &ExecutionFailure{Kind: "harness", Code: "session_event_binding"}
		}
		if event.HostError != nil {
			return submissionFailure(string(event.HostError.Code))
		}
		if event.IdleDisposition == wire.SessionIdleDispositionTurnError || event.Status == wire.SessionStatusError {
			return submissionFailure("")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session events: %w", err)
	}
	return &ExecutionFailure{Kind: "harness", Code: "session_events_closed", Retryable: true}
}

// transcriptTail retains settled history and refreshes its last row, which may
// receive a patch while a response is still being committed.
type transcriptTail struct {
	messages    []wire.Message
	initialized bool
}

func (t *transcriptTail) read(ctx context.Context, c *liveClient, sessionID string) ([]wire.Message, error) {
	position := "from=oldest"
	if t.initialized {
		position = "after_ord=0"
	}
	t.initialized = true
	if len(t.messages) > 0 {
		// Include the last row again rather than assuming its content is immutable.
		last := t.messages[len(t.messages)-1]
		position = fmt.Sprintf("after_ord=%d", max(0, last.Ord-1))
		t.messages = t.messages[:len(t.messages)-1]
	}
	for {
		req, err := c.newRequest(ctx, http.MethodGet, "/v1/sessions/"+sessionID+"/messages?"+position, nil)
		if err != nil {
			return nil, err
		}
		page, err := decodeJSON[wire.SessionTranscriptPage](c.do(req)) //nolint:bodyclose // decodeJSON closes the body.
		if err != nil {
			return nil, err
		}
		t.messages = append(t.messages, page.Messages...)
		if page.AfterCursor == "" {
			return t.messages, nil
		}
		next := "after=" + url.QueryEscape(page.AfterCursor)
		if next == position {
			return nil, fmt.Errorf("transcript page did not advance after cursor %q", page.AfterCursor)
		}
		position = next
	}
}

func (c *liveClient) unhandledInput(ctx context.Context, sessionID string) error {
	if c.onHumanInput != nil {
		return nil
	}
	pending, err := c.pendingCheckpoints(ctx, sessionID)
	if err != nil {
		return err
	}
	feedback, err := c.pendingFeedback(ctx, sessionID)
	if err != nil {
		return err
	}
	if len(pending) > 0 || feedback != nil {
		return &ExecutionFailure{Kind: "harness", Code: "operator_input_required"}
	}
	return nil
}
