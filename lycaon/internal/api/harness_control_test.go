package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHarnessControlsRegisterWithoutManualLLM(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store.NewMemory()}), nil, "harness-test-token")

	for _, path := range []string{"/harness/overlays", "/harness/upgrade-history"} {
		fixture := httptest.NewRecorder()
		srv.ServeHTTP(fixture, httptest.NewRequest(http.MethodPost, path, nil))
		if fixture.Code != http.StatusUnauthorized {
			t.Fatalf("fixture route %s status = %d want authenticated route", path, fixture.Code)
		}
	}

	manual := httptest.NewRecorder()
	manualRequest := httptest.NewRequest(http.MethodGet, "/harness/llm/pending", nil)
	manualRequest.Header.Set("Authorization", "Bearer harness-test-token")
	srv.ServeHTTP(manual, manualRequest)
	if manual.Code != http.StatusNotFound {
		t.Fatalf("manual LLM route status = %d want absent without provider", manual.Code)
	}
}

func TestHarnessPreviewPublishesThroughProjectEventHub(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	projects := project.NewMemoryRegistry()
	p, err := projects.Create(t.Context(), project.CreateParams{Draft: true, Name: "Preview fixture"})
	testutil.FailErr(t, "register preview project", err)
	sessions := store.NewMemory()
	sess, err := sessions.Create(t.Context(), wire.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "create session", err)
	hub := events.NewMemoryHub()
	eventsCh, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe project events", err)
	defer unsubscribe()

	srv := NewServer(requiredTestDeps(t, Dependencies{Projects: projects, Store: sessions, EventPublisher: &events.Publisher{Hub: hub}}), nil, "harness-test-token")
	payload, err := json.Marshal(wire.PreviewEvent{
		Op: wire.PreviewEventOpAttach, SessionID: sess.ID, PageID: "page-1", Seq: 1,
	})
	testutil.FailErr(t, "encode preview event", err)
	req := httptest.NewRequest(http.MethodPost, "/harness/preview", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer harness-test-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview route status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	envelope := testutil.Receive(t, "preview SSE event", eventsCh)
	if envelope.Topic != wire.EventTopicPreview {
		t.Fatalf("event topic = %q want preview", envelope.Topic)
	}
}

func TestHarnessControlsStayAbsentWithoutExplicitHarness(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "")
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store.NewMemory()}), nil, TestAPIToken)

	for _, path := range []string{"/harness/overlays", "/harness/upgrade-history"} {
		recorder := httptest.NewRecorder()
		srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("fixture route %s status = %d want absent outside harness", path, recorder.Code)
		}
	}
}

// The seeded secret payload takes its closed facts from the surface, so a
// harness card carries what a live screen would.
func TestHarnessSecretSeedDerivesFactsFromTheSurface(t *testing.T) {
	t.Parallel()
	cases := []struct {
		surface   string
		managed   bool
		wantKind  secretmatch.DestinationKind
		wantRedac bool
		wantBreak bool
	}{
		{"model_request", false, secretmatch.DestinationModelProvider, true, false},
		{"http_request", true, secretmatch.DestinationService, true, true},
		{"command", true, secretmatch.DestinationProcess, false, false},
	}
	for _, tc := range cases {
		cpReq := hitl.CheckpointRequest{ToolCallID: "call_1"}
		applyHarnessSecretScreen(&cpReq, harnessToolApprovalReq{
			Secret: &harnessSecretReq{Surface: tc.surface, Managed: tc.managed},
		}, "", "")
		screen := cpReq.SecretScreen
		if screen == nil {
			t.Fatalf("%s: no secret payload", tc.surface)
		}
		if screen.DestinationKind != tc.wantKind {
			t.Errorf("%s: destination kind = %q, want %q", tc.surface, screen.DestinationKind, tc.wantKind)
		}
		if screen.CanRedact != tc.wantRedac || screen.RedactionBreaks != tc.wantBreak {
			t.Errorf("%s: redaction facts = (%v, %v), want (%v, %v)",
				tc.surface, screen.CanRedact, screen.RedactionBreaks, tc.wantRedac, tc.wantBreak)
		}
		if screen.Managed != tc.managed {
			t.Errorf("%s: managed = %v", tc.surface, screen.Managed)
		}
		if cpReq.Explanation == nil || cpReq.Explanation.What == "" {
			t.Errorf("%s: seeded card has no impact sentence", tc.surface)
		}
	}
}

func TestHarnessResponsePreservesProviderStreamFailure(t *testing.T) {
	provider := llm.NewManualProvider()
	provider.SetAuto(false, "")
	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "start manual stream", err)
	pending, ok := provider.Pending(t.Context(), "", time.Second)
	if !ok {
		t.Fatal("manual request was not pending")
	}
	body, err := json.Marshal(map[string]any{
		"id": pending.ID,
		"stream_chunks": []map[string]any{
			{"content": "partial response"},
			{"error": "fixture provider disconnected", "done": true},
		},
	})
	testutil.FailErr(t, "encode failed response", err)
	server := NewServer(requiredTestDeps(t, Dependencies{Store: store.NewMemory(), ManualLLM: provider}), nil, "harness-test-token")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/harness/llm/respond", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	server.handleHarnessLLMRespond(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d: %s", response.Code, response.Body.String())
	}
	first := <-stream
	if first.Content != "partial response" || first.Err != nil || first.Done {
		t.Fatalf("partial chunk = %+v", first)
	}
	last := <-stream
	if last.Err == nil || last.Err.Error() != "fixture provider disconnected" || !last.Done {
		t.Fatalf("terminal chunk = %+v", last)
	}
	if _, open := <-stream; open {
		t.Fatal("failed stream stayed open")
	}
	if _, pending := provider.Pending(t.Context(), "", 0); pending {
		t.Fatal("failed stream retained its pending request")
	}
	if err := provider.RespondWithChunks(pending.ID, "duplicate", nil, nil); err == nil {
		t.Fatal("settled failure accepted a second response")
	}
}

func manualHarnessServer(t *testing.T) (*Server, *llm.ManualProvider) {
	t.Helper()
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	provider := llm.NewManualProvider()
	provider.SetAuto(false, "")
	return NewServer(requiredTestDeps(t, Dependencies{Store: store.NewMemory(), ManualLLM: provider}), nil, "harness-test-token"), provider
}

func manualHarnessRequest(s *Server, method, path, body string, authorized bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorized {
		request.Header.Set("Authorization", "Bearer harness-test-token")
	}
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	return response
}

func TestHarnessManualRoutesProtectAndPreservePendingRequest(t *testing.T) {
	server, provider := manualHarnessServer(t)
	calls := []wire.ToolCall{{ID: "call-1", Name: "inspect"}}
	messages := []wire.Message{{Role: wire.MessageRoleAssistant, Content: "Inspect this", ToolCalls: calls}}
	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{
		Model: "fixture-model", Debug: modelcall.RequestDebug{SessionID: "session-1"},
		Messages: messages, Tools: []tools.ToolMeta{{Name: "inspect"}},
	})
	testutil.FailErr(t, "start manual request", err)
	pending, ok := provider.Pending(t.Context(), "session-1", 0)
	if !ok {
		t.Fatal("request was not pending")
	}
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/harness/llm/pending", ""},
		{http.MethodPost, "/harness/llm/respond", `{"id":"` + pending.ID + `","content":"unauthorized"}`},
		{http.MethodPost, "/harness/llm/auto", `{"enabled":true,"text":"unauthorized"}`},
	} {
		response := manualHarnessRequest(server, route.method, route.path, route.body, false)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthorized status = %d", route.path, response.Code)
		}
	}
	if current, ok := provider.Pending(t.Context(), "session-1", 0); !ok || current.ID != pending.ID {
		t.Fatal("unauthorized control settled the pending request")
	}
	response := manualHarnessRequest(server, http.MethodGet, "/harness/llm/pending?session_id=%20session-1%20&wait=1", "", true)
	var dto harnessPendingDTO
	testutil.FailErr(t, "decode pending response", json.Unmarshal(response.Body.Bytes(), &dto))
	if response.Code != http.StatusOK || !dto.Pending || dto.ID != pending.ID || dto.SessionID != "session-1" || dto.Model != "fixture-model" || !reflect.DeepEqual(dto.Tools, []string{"inspect"}) || len(dto.Messages) != 1 || dto.Messages[0].Content != messages[0].Content || dto.Messages[0].Role != "assistant" || !reflect.DeepEqual(dto.Messages[0].ToolCalls, calls) {
		t.Fatalf("pending request lost provider facts: status=%d dto=%+v", response.Code, dto)
	}
	absent := manualHarnessRequest(server, http.MethodGet, "/harness/llm/pending?session_id=other", "", true)
	if absent.Code != http.StatusOK || strings.TrimSpace(absent.Body.String()) != `{"pending":false}` {
		t.Fatalf("foreign session pending = %d %s", absent.Code, absent.Body.String())
	}
	for _, invalid := range []struct {
		body   string
		status int
	}{{`{`, http.StatusBadRequest}, {`{}`, http.StatusBadRequest}, {`{"id":"unknown"}`, http.StatusNotFound}} {
		body := invalid.body
		rejected := manualHarnessRequest(server, http.MethodPost, "/harness/llm/respond", body, true)
		if rejected.Code != invalid.status {
			t.Fatalf("invalid response %q = %d %s", body, rejected.Code, rejected.Body.String())
		}
		if current, ok := provider.Pending(t.Context(), "session-1", 0); !ok || current.ID != pending.ID {
			t.Fatal("invalid response settled the request")
		}
	}
	body := `{"id":"` + pending.ID + `","content":"accepted"}`
	accepted := manualHarnessRequest(server, http.MethodPost, "/harness/llm/respond", body, true)
	if accepted.Code != http.StatusOK {
		t.Fatalf("valid response = %d %s", accepted.Code, accepted.Body.String())
	}
	chunk := testutil.Receive(t, "manual response", stream)
	if chunk.Content != "accepted" || !chunk.Done || chunk.Err != nil {
		t.Fatalf("provider response = %+v", chunk)
	}
	if _, open := <-stream; open {
		t.Fatal("settled response stream remained open")
	}
	duplicate := manualHarnessRequest(server, http.MethodPost, "/harness/llm/respond", body, true)
	if duplicate.Code != http.StatusNotFound {
		t.Fatalf("duplicate response = %d", duplicate.Code)
	}
}

func TestHarnessAutoControlSettlesWaitingTurnAndCanReturnToManual(t *testing.T) {
	server, provider := manualHarnessServer(t)
	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "start waiting turn", err)
	malformed := manualHarnessRequest(server, http.MethodPost, "/harness/llm/auto", `{`, true)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed auto = %d", malformed.Code)
	}
	if _, ok := provider.Pending(t.Context(), "", 0); !ok {
		t.Fatal("malformed auto control settled waiting turn")
	}
	response := manualHarnessRequest(server, http.MethodPost, "/harness/llm/auto", `{"enabled":true,"text":"resume waiting turn"}`, true)
	if response.Code != http.StatusOK {
		t.Fatalf("enable auto = %d %s", response.Code, response.Body.String())
	}
	chunk := testutil.Receive(t, "resumed waiting turn", stream)
	if chunk.Content != "resume waiting turn" || !chunk.Done {
		t.Fatalf("resumed turn = %+v", chunk)
	}
	if _, open := <-stream; open {
		t.Fatal("resumed turn remained open")
	}
	if _, ok := provider.Pending(t.Context(), "", 0); ok {
		t.Fatal("auto control retained pending turn")
	}
	completion, err := provider.Complete(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "complete auto turn", err)
	if completion.Content != "resume waiting turn" {
		t.Fatalf("auto reply = %q", completion.Content)
	}
	disabled := manualHarnessRequest(server, http.MethodPost, "/harness/llm/auto", `{"enabled":false}`, true)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable auto = %d", disabled.Code)
	}
	manual, err := provider.Stream(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "start restored manual turn", err)
	pending, ok := provider.Pending(t.Context(), "", 0)
	if !ok {
		t.Fatal("disabled auto did not restore manual waiting")
	}
	testutil.FailErr(t, "settle restored turn", provider.RespondWithChunks(pending.ID, "manual again", nil, nil))
	chunk = testutil.Receive(t, "restored manual response", manual)
	if chunk.Content != "manual again" || !chunk.Done {
		t.Fatalf("restored response = %+v", chunk)
	}
	if _, open := <-manual; open {
		t.Fatal("restored manual stream remained open")
	}
}
