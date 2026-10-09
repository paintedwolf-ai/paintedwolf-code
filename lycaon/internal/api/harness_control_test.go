package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHarnessControlsRegisterWithoutManualLLM(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	srv := NewServer(requiredTestDeps(t, Dependencies{Core: CoreDependencies{Store: store.NewMemory()}}), nil, "harness-test-token")

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

	srv := NewServer(requiredTestDeps(t, Dependencies{Core: CoreDependencies{Projects: projects, Store: sessions}, Host: HostDependencies{EventPublisher: &events.Publisher{Hub: hub}}}), nil, "harness-test-token")
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
	srv := NewServer(requiredTestDeps(t, Dependencies{Core: CoreDependencies{Store: store.NewMemory()}}), nil, TestAPIToken)

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
	server := NewServer(requiredTestDeps(t, Dependencies{Core: CoreDependencies{Store: store.NewMemory()}, Harness: HarnessDependencies{ManualLLM: provider}}), nil, "harness-test-token")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/harness/llm/respond", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	server.Routes.HarnessControl.handleHarnessLLMRespond(response, request)
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
