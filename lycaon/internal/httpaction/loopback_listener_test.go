package httpaction

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubLoopbackObserver struct {
	facts []*api.LoopbackListenerFact
	calls int
}

func (s *stubLoopbackObserver) RecordSessionContainer(string, string, string) {}

func (s *stubLoopbackObserver) ObserveLoopbackEndpoint(_ context.Context, _, _ string, _ netip.AddrPort) *api.LoopbackListenerFact {
	if len(s.facts) == 0 {
		return nil
	}
	idx := s.calls
	s.calls++
	if idx >= len(s.facts) {
		idx = len(s.facts) - 1
	}
	return s.facts[idx]
}

func TestHTTPRequestLoopbackListenerAttributionOtherSessionAndReplacement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}

	t.Run("other_session_holder_raises_guidance_code", func(t *testing.T) {
		obs := &stubLoopbackObserver{
			facts: []*api.LoopbackListenerFact{
				{
					Kind:             "other_session",
					Address:          "127.0.0.1:8000",
					ConnectedAddress: "127.0.0.1:8000",
					PID:              401,
					ProcessName:      "python3",
					SessionID:        "session-ttt",
					SessionTitle:     "Tic Tac Toe Game with AI",
					ProjectID:        "proj-ttt",
					ProjectName:      "Tic-Tac-Toe",
					Handle:           "bg-1",
					Command:          "python3 -m http.server 8000",
					Background:       true,
					Description:      "Port 8000 is served by `python3 -m http.server 8000`, a background command in the chat 'Tic Tac Toe Game with AI' in the project 'Tic-Tac-Toe'.",
					Holders: []api.LoopbackListenerHolder{
						{
							Kind:         "other_session",
							Address:      "127.0.0.1:8000",
							PID:          401,
							ProcessName:  "python3",
							SessionID:    "session-ttt",
							SessionTitle: "Tic Tac Toe Game with AI",
							ProjectID:    "proj-ttt",
							ProjectName:  "Tic-Tac-Toe",
							Handle:       "bg-1",
							Command:      "python3 -m http.server 8000",
							Background:   true,
						},
					},
				},
			},
		}
		outcome := &tools.ToolInvocationOut{}
		raw, err := registry.Run(t.Context(), "http_request", map[string]any{
			"url":                server.URL,
			"capability_request": loopbackCapability(t, server.URL),
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID: "session-current",
				Agent:     toolprofiles.DefaultToolProfileID,
			},
			Local:   tools.InvocationLocal{ContainerRecorder: obs},
			Effects: tools.InvocationEffects{Out: outcome},
		})
		if err != nil {
			t.Fatalf("run http_request: %v", err)
		}
		var parsed result
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if parsed.Listener == nil || parsed.Listener.Kind != "other_session" || parsed.Listener.SessionTitle != "Tic Tac Toe Game with AI" {
			t.Fatalf("unexpected listener in result JSON: %+v", parsed.Listener)
		}
		if outcome.Listener == nil || outcome.Listener.Handle != "bg-1" {
			t.Fatalf("unexpected sidecar listener: %+v", outcome.Listener)
		}
		if !outcome.Facts.HasCode(toolrejection.HTTPRequestLoopbackOtherSessionCode) {
			t.Fatalf("expected %s to be raised, got %+v", toolrejection.HTTPRequestLoopbackOtherSessionCode, outcome.Facts)
		}
	})

	t.Run("listener_replaced_between_pre_and_post_is_marked_ambiguous", func(t *testing.T) {
		obs := &stubLoopbackObserver{
			facts: []*api.LoopbackListenerFact{
				{
					Kind: "this_session",
					PID:  500,
					Holders: []api.LoopbackListenerHolder{
						{Kind: "this_session", Address: "127.0.0.1:8000", PID: 500, SessionID: "session-current", Command: "node server.js"},
					},
				},
				{
					Kind: "other_session",
					PID:  600,
					Holders: []api.LoopbackListenerHolder{
						{Kind: "other_session", Address: "127.0.0.1:8000", PID: 600, SessionID: "session-other", Command: "python3 -m http.server 8000"},
					},
				},
			},
		}
		outcome := &tools.ToolInvocationOut{}
		raw, err := registry.Run(t.Context(), "http_request", map[string]any{
			"url":                server.URL,
			"capability_request": loopbackCapability(t, server.URL),
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID: "session-current",
				Agent:     toolprofiles.DefaultToolProfileID,
			},
			Local:   tools.InvocationLocal{ContainerRecorder: obs},
			Effects: tools.InvocationEffects{Out: outcome},
		})
		if err != nil {
			t.Fatalf("run http_request: %v", err)
		}
		var parsed result
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if parsed.Listener == nil || !parsed.Listener.Ambiguous || !parsed.Listener.Replaced {
			t.Fatalf("expected ambiguous=true and replaced=true, got %+v", parsed.Listener)
		}
		if parsed.Listener.Kind != "" || parsed.Listener.PID != 0 || len(parsed.Listener.Holders) != 2 {
			t.Fatalf("expected 2 holders without guessing single identity, got %+v", parsed.Listener)
		}
		if !outcome.Facts.HasCode(toolrejection.HTTPRequestLoopbackOtherSessionCode) {
			t.Fatalf("expected %s when replaced holder includes other_session", toolrejection.HTTPRequestLoopbackOtherSessionCode)
		}
	})

	t.Run("container_mismatch_raises_guidance_code", func(t *testing.T) {
		obs := &stubLoopbackObserver{
			facts: []*api.LoopbackListenerFact{
				{
					Kind:              "external_process",
					Address:           "[::1]:8000",
					PID:               777,
					ProcessName:       "caddy",
					ContainerMismatch: true,
					PublishedContainer: &api.LoopbackPublishedContainer{
						ContainerID:   "cid-1",
						ContainerName: "web-1",
						HostIP:        "127.0.0.1",
						HostPort:      8000,
						LoopbackOnly:  true,
					},
					Description: "Port 8000 is served by an external process `caddy` (PID 777). Container `web-1` from this chat publishes 127.0.0.1:8000->8000/tcp, while another listener also holds port 8000.",
					Holders: []api.LoopbackListenerHolder{
						{Kind: "external_process", Address: "[::1]:8000", PID: 777, ProcessName: "caddy"},
					},
				},
			},
		}
		outcome := &tools.ToolInvocationOut{}
		if _, err := registry.Run(t.Context(), "http_request", map[string]any{
			"url":                server.URL,
			"capability_request": loopbackCapability(t, server.URL),
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID: "session-current",
				Agent:     toolprofiles.DefaultToolProfileID,
			},
			Local:   tools.InvocationLocal{ContainerRecorder: obs},
			Effects: tools.InvocationEffects{Out: outcome},
		}); err != nil {
			t.Fatalf("run http_request: %v", err)
		}
		if outcome.Listener == nil || !outcome.Listener.ContainerMismatch || outcome.Listener.PublishedContainer == nil {
			t.Fatalf("expected container_mismatch on listener fact, got %+v", outcome.Listener)
		}
		if !outcome.Facts.HasCode(toolrejection.HTTPRequestLoopbackOtherSessionCode) {
			t.Fatalf("expected %s on container mismatch", toolrejection.HTTPRequestLoopbackOtherSessionCode)
		}
	})
}
