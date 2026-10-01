package security

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestChatStreamingTwoChannelE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()

	// Create the project before subscribing.
	project := createAPIProjectAtPath(t, base, projectDir)
	if project.ID == "" {
		t.Fatal("open project returned empty id")
	}

	// Create a session under the project.
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	// Subscribe before prompt admission.
	hubCtx, hubCancel := context.WithCancel(t.Context())
	defer hubCancel()
	hubEvents := subscribeProjectEvents(t, hubCtx, base, project.ID)

	// Resolve the replay URL after the turn settles.
	acceptPromptOpenAPI(t, base, sess.ID, `{"text":"hello stream"}`)
	messageID, streamURL := lastAssistantStreamURL(t, base, sess.ID, 10*time.Second)
	if messageID == "" || streamURL == "" {
		t.Fatal("assistant stream URL missing after prompt turn")
	}
	if !strings.HasPrefix(streamURL, "/v1/sessions/"+sess.ID+"/stream?message=") {
		t.Fatalf("stream_url should be a per-message URL under the session: %q", streamURL)
	}

	// Drain the replay stream.
	tokens, gotDone := drainPerMessageStream(t, base+streamURL, 5*time.Second)
	if !gotDone {
		t.Fatalf("per-message stream ended without {\"done\":true} envelope (tokens=%q)", tokens)
	}
	if len(tokens) == 0 {
		t.Fatal("per-message stream produced no token chunks before done")
	}
	streamed := strings.TrimSpace(strings.Join(tokens, ""))
	if streamed == "" {
		t.Fatal("joined token stream is empty")
	}

	// Flush debounced project events.
	if hub := h.MemoryHub(); hub != nil {
		hub.FlushDebounced()
	}
	collected := collectHubUntil(t, hubEvents, 5*time.Second, func(seen map[string]int) bool {
		return seen[string(wire.EventTopicSession)] >= 1
	})
	hubCancel()

	sessionEnvelopes := collected[string(wire.EventTopicSession)]
	if len(sessionEnvelopes) == 0 {
		t.Fatal("project hub did not deliver a session envelope")
	}

	// User turns omit host-loop events.
	for i, env := range collected[string(wire.EventTopicLLM)] {
		var raw map[string]any
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			t.Fatalf("llm envelope #%d data is not a JSON object: %v (data=%s)", i, err, env.Data)
		}
		if _, hasToken := raw["token"]; hasToken {
			t.Fatalf("llm envelope #%d carries a `token` field — tokens belong on the per-message stream URL, not the project hub: %v", i, raw)
		}
	}

	// Compare replay with durable messages.
	msgs := listMessagesOpenAPI(t, base, map[string]string{"id": sess.ID}, http.StatusOK)
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (user + assistant), got %d: %+v", len(msgs), msgs)
	}
	var sawUser, sawAssistant bool
	var assistantContent string
	for _, m := range msgs {
		switch m.Role {
		case wire.MessageRoleUser:
			if m.Content == "hello stream" {
				sawUser = true
			}
		case wire.MessageRoleAssistant:
			sawAssistant = true
			if m.ID == messageID {
				assistantContent = m.Content
			}
		case wire.MessageRoleTool, wire.MessageRoleSystem:
		}
	}
	if !sawUser {
		t.Fatal("user prompt not persisted to /messages")
	}
	if !sawAssistant {
		t.Fatal("assistant reply not persisted to /messages")
	}
	if assistantContent == "" {
		t.Fatalf("assistant message %s not found in /messages", messageID)
	}
	// Ignore token-boundary whitespace.
	if canonicalize(streamed) != canonicalize(assistantContent) {
		t.Fatalf("streamed text does not match persisted assistant content:\n  streamed = %q\n  stored   = %q", streamed, assistantContent)
	}
}

func getJSON[T any](t *testing.T, base, path string, wantStatus int) T {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	testutil.FailErr(t, "http.NewRequest failed", err)
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d; body = %s", path, resp.StatusCode, wantStatus, respBody)
	}
	var out T
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("decode GET %s: %v body=%s", path, err, respBody)
	}
	return out
}

// streamChunk is one replay envelope.
type streamChunk struct {
	Token string `json:"token,omitempty"`
	Done  bool   `json:"done"`
}

// drainPerMessageStream collects tokens through the terminal envelope.
func drainPerMessageStream(t *testing.T, url string, deadline time.Duration) ([]string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open per-message stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("per-message stream status = %d body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("per-message stream Content-Type = %q, want text/event-stream", ct)
	}

	var tokens []string
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return tokens, false
			}
			// Cancellation appears as a network error.
			return tokens, false
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("decode token chunk: %v line=%q", err, payload)
		}
		if chunk.Done {
			return tokens, true
		}
		if chunk.Token != "" {
			tokens = append(tokens, chunk.Token)
		}
	}
}

// subscribeProjectEvents forwards parsed project envelopes.
func subscribeProjectEvents(t *testing.T, ctx context.Context, base, projectID string) <-chan wire.EventEnvelope {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/events?project_id="+projectID, nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // The SSE reader closes resp.Body.
	if err != nil {
		t.Fatalf("subscribe events: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("events subscribe status = %d body = %s", resp.StatusCode, body)
	}

	ch := make(chan wire.EventEnvelope, 64)
	var once sync.Once
	closeCh := func() { close(ch) }
	go func() {
		defer resp.Body.Close()
		defer once.Do(closeCh)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			var env wire.EventEnvelope
			if err := json.Unmarshal([]byte(payload), &env); err != nil {
				continue
			}
			select {
			case ch <- env:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// collectHubUntil groups envelopes until a predicate matches.
func collectHubUntil(t *testing.T, ch <-chan wire.EventEnvelope, deadline time.Duration, satisfied func(map[string]int) bool) map[string][]wire.EventEnvelope {
	t.Helper()
	out := map[string][]wire.EventEnvelope{}
	counts := map[string]int{}
	timer := time.NewTimer(testutil.Timeout(deadline))
	defer timer.Stop()
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return out
			}
			topic := string(env.Topic)
			out[topic] = append(out[topic], env)
			counts[topic]++
			if satisfied(counts) {
				return out
			}
		case <-timer.C:
			return out
		}
	}
}

// canonicalize collapses token-boundary whitespace.
func canonicalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
