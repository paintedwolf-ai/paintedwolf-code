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
	project := createAPIProjectAtPath(t, base, projectDir)
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	subCtx, subCancel := context.WithCancel(t.Context())
	defer subCancel()
	hub := newProjectHub(t, subCtx, base, project.ID)

	// Prompt events.
	acceptPromptOpenAPI(t, base, sess.ID, `{"text":"hub probe"}`)

	sessionEnv := hub.waitForField(t, wire.EventTopicSession, "last_message", 10*time.Second)
	assertField(t, sessionEnv, "id", sess.ID)
	assertField(t, sessionEnv, "status", "") // status is enum string but value varies; just assert key present
	assertField(t, sessionEnv, "last_message", "")

	msgEnv := hub.waitFor(t, wire.EventTopicMessage, 3*time.Second)
	assertField(t, msgEnv, "session_id", sess.ID)
	assertField(t, msgEnv, "op", "")
	assertField(t, msgEnv, "message", "")

	// Host loop event.
	acceptPromptOpenAPI(t, base, sess.ID, `{"text":"`+surface.HostLoopWakeSentinel+`"}`)
	if memHub := h.MemoryHub(); memHub != nil {
		memHub.FlushDebounced()
	}
	llmEnv := hub.waitFor(t, wire.EventTopicLLM, 5*time.Second)
	assertField(t, llmEnv, "call_id", "")
	assertField(t, llmEnv, "status", "")
	assertFieldAbsent(t, llmEnv, "token") // no per-token deltas here

	// Workflow event.
	exitAmbientRunHTTP(t, h.Server, sess.ID)
	hub.waitFor(t, wire.EventTopicWorkflow, 3*time.Second) // drain ambient exit envelope
	run := journeyPostJSON[wire.WorkflowRun](t, base,
		"/v1/sessions/"+sess.ID+"/workflow-runs",
		`{"workflow_id":"plan","workflow_version":"1.0.0"}`, http.StatusCreated)

	wfEnv := hub.waitForWorkflowRunID(t, run.ID, 3*time.Second)
	assertField(t, wfEnv, "workflow_id", "plan")
	assertField(t, wfEnv, "workflow_run_id", run.ID)
	assertField(t, wfEnv, "status", "")

	// Provider events.
	_ = openAPIPostJSON[wire.ProviderMeta](t, base, "/v1/providers", nil,
		`{"id":"sse-test","base_url":"https://x.example/v1","models":[{"id":"m1"}]}`, http.StatusCreated)
	createdEnv := hub.waitFor(t, wire.EventTopicProviders, 3*time.Second)
	assertField(t, createdEnv, "provider_id", "sse-test")
	assertField(t, createdEnv, "action", "created")
	journeyPatch(t, base, "/v1/providers/sse-test",
		`{"base_url":"https://x2.example/v1","models":[{"id":"m1"}]}`, http.StatusOK)
	provEnv := hub.waitFor(t, wire.EventTopicProviders, 3*time.Second)
	assertField(t, provEnv, "provider_id", "sse-test")
	assertField(t, provEnv, "action", "updated")

	// Model policy event.
	policyProvider, _ := seedTestProvider(t, base, false)
	policy := wire.ModelPolicy{
		Coordinator: &wire.ModelRefDTO{ProviderID: policyProvider, Model: testProviderModel},
		Lite:        &wire.ModelRefDTO{ProviderID: policyProvider, Model: testProviderModel},
		AgentPool: wire.AgentPoolDTO{
			Selection: "round_robin",
			Models:    []wire.ModelRefDTO{{ProviderID: policyProvider, Model: testProviderModel}},
		},
	}
	policyBody, err := json.Marshal(policy)
	testutil.FailErr(t, "marshal policy", err)
	journeyPatch(t, base, "/v1/settings/model-policy", string(policyBody), http.StatusOK)
	mpEnv := hub.waitFor(t, wire.EventTopicModelPolicy, 3*time.Second)
	assertField(t, mpEnv, "scope", "global")
	assertField(t, mpEnv, "action", "updated")

	// Settings event.
	journeyPatch(t, base, "/v1/settings/approvals",
		`{"rules":[],"approval_posture":"strict"}`, http.StatusOK)
	setEnv := hub.waitForSettingsArea(t, "approvals", 5*time.Second)
	assertField(t, setEnv, "area", "approvals")
	assertField(t, setEnv, "scope", "global")
	assertField(t, setEnv, "action", "updated")

	// Queue event.
	queue := openAPIGetJSON[wire.QueueDraft](t, base, "/v1/sessions/{id}/queue", map[string]string{"id": sess.ID}, http.StatusOK)
	hold := true
	queueBody, err := json.Marshal(wire.QueueMutateRequest{Op: "hold", Hold: &hold, ExpectedRevision: queue.Revision})
	testutil.FailErr(t, "encode queue hold", err)
	journeyPatch(t, base, "/v1/sessions/"+sess.ID+"/queue", string(queueBody), http.StatusOK)
	queueEnv := hub.waitFor(t, wire.EventTopicQueue, 3*time.Second)
	assertField(t, queueEnv, "revision", "")
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
func (h *projectHub) waitFor(t *testing.T, topic wire.EventTopic, deadline time.Duration) wire.EventEnvelope {
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

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case env, ok := <-h.ch:
			if !ok {
				t.Fatalf("event stream closed before topic %q arrived", topic)
			}
			if env.Topic == topic {
				return env
			}
			h.mu.Lock()
			h.queued = append(h.queued, env)
			h.mu.Unlock()
		case <-timer.C:
			t.Fatalf("timeout waiting for topic %q (queued topics: %v)", topic, h.queuedTopics())
		}
	}
}

// waitForField skips earlier envelopes that lack the requested field.
func (h *projectHub) waitForField(t *testing.T, topic wire.EventTopic, field string, deadline time.Duration) wire.EventEnvelope {
	t.Helper()
	deadlineAt := time.Now().Add(deadline)
	for {
		remaining := time.Until(deadlineAt)
		if remaining <= 0 {
			t.Fatalf("timeout waiting for topic %q field %q", topic, field)
		}
		env := h.waitFor(t, topic, remaining)
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
func (h *projectHub) waitForWorkflowRunID(t *testing.T, runID string, deadline time.Duration) wire.EventEnvelope {
	t.Helper()
	runID = strings.TrimSpace(runID)
	deadlineAt := time.Now().Add(deadline)
	for {
		remaining := time.Until(deadlineAt)
		if remaining <= 0 {
			t.Fatalf("timeout waiting for workflow_run_id %q", runID)
		}
		env := h.waitFor(t, wire.EventTopicWorkflow, remaining)
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
func (h *projectHub) waitForSettingsArea(t *testing.T, area string, deadline time.Duration) wire.EventEnvelope {
	t.Helper()
	deadlineAt := time.Now().Add(deadline)
	var seen []string
	for {
		remaining := time.Until(deadlineAt)
		if remaining <= 0 {
			t.Fatalf("timeout waiting for settings area %q (saw: %v)", area, seen)
		}
		env := h.waitFor(t, wire.EventTopicSettings, remaining)
		var raw map[string]any
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			t.Fatalf("settings data not an object: %v", err)
		}
		got := strings.TrimSpace(stringField(raw, "area"))
		if got == area {
			return env
		}
		seen = append(seen, got)
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

func (h *projectHub) queuedTopics() []wire.EventTopic {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]wire.EventTopic, 0, len(h.queued))
	for _, env := range h.queued {
		out = append(out, env.Topic)
	}
	return out
}

// assertField requires a field and optionally its value.
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
	if want == "" {
		return
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
