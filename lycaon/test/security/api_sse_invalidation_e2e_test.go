package security

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestSSETopicInvalidationE2E(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate provider credential writes

	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	projectDir := t.TempDir()
	project := CreateAPIProjectAtPath(t, base, projectDir)
	sess := OpenAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	subCtx, subCancel := context.WithCancel(t.Context())
	defer subCancel()
	hub := newProjectHub(t, subCtx, base, project.ID)

	// Prompt events.
	acceptPromptOpenAPI(t, base, sess.ID, `{"text":"hub probe"}`)

	sessionEvent := decodeEnvelope[wire.SessionEvent](t, hub.waitForField(t, wire.EventTopicSession, "last_message"))
	if sessionEvent.ID != sess.ID || sessionEvent.ProjectID != project.ID {
		t.Fatalf("session event = %+v, want session %s in project %s", sessionEvent, sess.ID, project.ID)
	}
	if sessionEvent.Status != wire.SessionStatusBusy && sessionEvent.Status != wire.SessionStatusIdle {
		t.Fatalf("session event status = %q, want busy or idle", sessionEvent.Status)
	}

	msgEvent := decodeEnvelope[wire.MessageEvent](t, hub.waitFor(t, wire.EventTopicMessage))
	if msgEvent.SessionID != sess.ID || (msgEvent.Op != wire.MessageChangeAppend && msgEvent.Op != wire.MessageChangePatch) {
		t.Fatalf("message event = session %q op %q, want %s append or patch", msgEvent.SessionID, msgEvent.Op, sess.ID)
	}
	transcript := listMessagesOpenAPI(t, base, map[string]string{"id": sess.ID}, http.StatusOK)
	if !transcriptHas(transcript, func(m wire.Message) bool { return m.ID == msgEvent.Message.ID }) {
		t.Fatalf("message event names %q, which is not in the transcript", msgEvent.Message.ID)
	}
	if !transcriptHas(transcript, func(m wire.Message) bool {
		return sessionEvent.LastMessage != "" && strings.HasPrefix(m.Content, sessionEvent.LastMessage)
	}) {
		t.Fatalf("session event last_message %q matches no transcript message", sessionEvent.LastMessage)
	}

	// Host loop event.
	acceptPromptOpenAPI(t, base, sess.ID, `{"text":"`+surface.HostLoopWakeSentinel+`"}`)
	if memHub := h.MemoryHub(); memHub != nil {
		memHub.FlushDebounced()
	}
	llmEnv := hub.waitFor(t, wire.EventTopicLLM)
	llmEvent := decodeEnvelope[wire.LLMCallEvent](t, llmEnv)
	if llmEvent.CallID == "" || llmEvent.SessionID != sess.ID {
		t.Fatalf("llm event = %+v, want a call of session %s", llmEvent, sess.ID)
	}
	if llmEvent.Status != wire.LLMCallStatusActive && llmEvent.Status != wire.LLMCallStatusOK {
		t.Fatalf("llm event status = %q, want active or ok", llmEvent.Status)
	}
	assertFieldAbsent(t, llmEnv, "token") // no per-token deltas here

	// Workflow event.
	exitAmbientRunHTTP(t, h.Server, sess.ID)
	hub.waitFor(t, wire.EventTopicWorkflow) // drain ambient exit envelope
	run := JourneyPostJSON[wire.WorkflowRun](t, base,
		"/v1/sessions/"+sess.ID+"/workflow-runs",
		`{"workflow_id":"plan","workflow_version":"1.0.1"}`, http.StatusCreated)

	wfEvent := decodeEnvelope[wire.WorkflowEvent](t, hub.waitForWorkflowRunID(t, run.ID))
	if wfEvent.WorkflowID != "plan" || wfEvent.WorkflowRunID != run.ID || wfEvent.Status != string(run.Status) {
		t.Fatalf("workflow event = %s/%s status %q, want plan/%s status %q",
			wfEvent.WorkflowID, wfEvent.WorkflowRunID, wfEvent.Status, run.ID, run.Status)
	}

	// Provider events.
	_ = OpenAPIPostJSON[wire.ProviderMeta](t, base, "/v1/providers", nil,
		`{"id":"sse-test","base_url":"https://x.example/v1","models":[{"id":"m1"}]}`, http.StatusCreated)
	createdEnv := hub.waitFor(t, wire.EventTopicProviders)
	assertField(t, createdEnv, "provider_id", "sse-test")
	assertField(t, createdEnv, "action", "created")
	journeyPatch(t, base, "/v1/providers/sse-test",
		`{"base_url":"https://x2.example/v1","models":[{"id":"m1"}]}`, http.StatusOK)
	provEnv := hub.waitFor(t, wire.EventTopicProviders)
	assertField(t, provEnv, "provider_id", "sse-test")
	assertField(t, provEnv, "action", "updated")

	// Model policy event.
	policyProvider, _ := SeedTestProvider(t, base, false)
	policy := wire.ModelPolicy{
		Coordinator: &wire.ModelRefDTO{ProviderID: policyProvider, Model: TestProviderModel},
		Lite:        &wire.ModelRefDTO{ProviderID: policyProvider, Model: TestProviderModel},
		AgentPool: wire.AgentPoolDTO{
			Selection: "round_robin",
			Models:    []wire.ModelRefDTO{{ProviderID: policyProvider, Model: TestProviderModel}},
		},
	}
	policyBody, err := json.Marshal(policy)
	testutil.FailErr(t, "marshal policy", err)
	journeyPatch(t, base, "/v1/settings/model-policy", string(policyBody), http.StatusOK)
	mpEnv := hub.waitFor(t, wire.EventTopicModelPolicy)
	assertField(t, mpEnv, "scope", "global")
	assertField(t, mpEnv, "action", "updated")

	// Settings event.
	journeyPatch(t, base, "/v1/settings/approvals",
		`{"rules":[],"approval_posture":"strict"}`, http.StatusOK)
	setEnv := hub.waitForSettingsArea(t, "approvals")
	assertField(t, setEnv, "area", "approvals")
	assertField(t, setEnv, "scope", "global")
	assertField(t, setEnv, "action", "updated")

	// Queue event.
	queue := OpenAPIGetJSON[wire.QueueDraft](t, base, "/v1/sessions/{id}/queue", map[string]string{"id": sess.ID}, http.StatusOK)
	hold := true
	queueBody, err := json.Marshal(wire.QueueMutateRequest{Op: "hold", Hold: &hold, ExpectedRevision: queue.Revision})
	testutil.FailErr(t, "encode queue hold", err)
	journeyPatch(t, base, "/v1/sessions/"+sess.ID+"/queue", string(queueBody), http.StatusOK)
	held := OpenAPIGetJSON[wire.QueueDraft](t, base, "/v1/sessions/{id}/queue", map[string]string{"id": sess.ID}, http.StatusOK)
	// Earlier activity may have published queue events at the starting revision.
	queueEvent := decodeEnvelope[wire.QueueEvent](t, hub.waitFor(t, wire.EventTopicQueue))
	for queueEvent.Revision <= queue.Revision {
		queueEvent = decodeEnvelope[wire.QueueEvent](t, hub.waitFor(t, wire.EventTopicQueue))
	}
	if queueEvent.Revision != held.Revision {
		t.Fatalf("queue event revision = %d, want the held queue's %d", queueEvent.Revision, held.Revision)
	}
}

func decodeEnvelope[T any](t *testing.T, env wire.EventEnvelope) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("topic %s data does not decode as %T: %v (data=%s)", env.Topic, out, err, env.Data)
	}
	return out
}

func transcriptHas(msgs []wire.Message, match func(wire.Message) bool) bool {
	for _, m := range msgs {
		if match(m) {
			return true
		}
	}
	return false
}

type projectHub struct {
	ch     <-chan wire.EventEnvelope
	mu     sync.Mutex
	queued []wire.EventEnvelope
}

func newProjectHub(t *testing.T, ctx context.Context, base, projectID string) *projectHub {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/v1/events?project_id="+projectID, nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // The SSE reader closes resp.Body.
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("subscribe status = %d body = %s", resp.StatusCode, body)
	}

	ch := make(chan wire.EventEnvelope, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
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
	return &projectHub{ch: ch}
}

// waitFor preserves envelopes from other topics.
func (h *projectHub) waitFor(t *testing.T, topic wire.EventTopic) wire.EventEnvelope {
	t.Helper()

	// Check the already-queued buffer first.
	h.mu.Lock()
	for i, env := range h.queued {
		if env.Topic == topic {
			h.queued = append(h.queued[:i], h.queued[i+1:]...)
			h.mu.Unlock()
			return env
		}
	}
	h.mu.Unlock()

	for {
		env := testutil.Receive(t, fmt.Sprintf("an event on topic %q", topic), h.ch)
		if env.Topic == topic {
			return env
		}
		h.mu.Lock()
		h.queued = append(h.queued, env)
		h.mu.Unlock()
	}
}

// waitForField skips earlier envelopes that lack the requested field.
func (h *projectHub) waitForField(t *testing.T, topic wire.EventTopic, field string) wire.EventEnvelope {
	t.Helper()
	for {
		env := h.waitFor(t, topic)
		var raw map[string]any
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			t.Fatalf("topic %s data not an object: %v", topic, err)
		}
		if _, exists := raw[field]; exists {
			return env
		}
	}
}

// waitForWorkflowRunID selects one run's workflow envelope.
func (h *projectHub) waitForWorkflowRunID(t *testing.T, runID string) wire.EventEnvelope {
	t.Helper()
	runID = strings.TrimSpace(runID)
	for {
		env := h.waitFor(t, wire.EventTopicWorkflow)
		var raw map[string]any
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			t.Fatalf("workflow data not an object: %v", err)
		}
		if strings.TrimSpace(stringField(raw, "workflow_run_id")) == runID {
			return env
		}
	}
}

// waitForSettingsArea selects one settings area.
func (h *projectHub) waitForSettingsArea(t *testing.T, area string) wire.EventEnvelope {
	t.Helper()
	for {
		env := h.waitFor(t, wire.EventTopicSettings)
		var raw map[string]any
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			t.Fatalf("settings data not an object: %v", err)
		}
		if strings.TrimSpace(stringField(raw, "area")) == area {
			return env
		}
	}
}

func stringField(raw map[string]any, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// assertField requires a string field with the given value.
func assertField(t *testing.T, env wire.EventEnvelope, field, want string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		t.Fatalf("topic %s data not an object: %v (data=%s)", env.Topic, err, env.Data)
	}
	v, ok := raw[field]
	if !ok {
		t.Fatalf("topic %s envelope missing field %q (data=%v)", env.Topic, field, raw)
	}
	got, _ := v.(string)
	if got != want {
		t.Fatalf("topic %s field %q = %v, want %q", env.Topic, field, v, want)
	}
}

func assertFieldAbsent(t *testing.T, env wire.EventEnvelope, field string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		t.Fatalf("topic %s data not an object: %v", env.Topic, err)
	}
	if v, present := raw[field]; present {
		t.Fatalf("topic %s envelope must NOT carry field %q (got %v)", env.Topic, field, v)
	}
}

func journeyPatch(t *testing.T, base, path, body string, wantStatus int) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+path, strings.NewReader(body))
	testutil.FailErr(t, "http.NewRequest failed", err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("PATCH %s status = %d, want %d; body = %s", path, resp.StatusCode, wantStatus, out)
	}
}
