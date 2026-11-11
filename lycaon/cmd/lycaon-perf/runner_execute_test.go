package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// promptServer answers the engine routes a sidecar workload drives. Each prompt
// adds one assistant message unless a test withholds it.
type promptServer struct {
	mu          sync.Mutex
	messages    []api.Message
	status      api.SessionStatus
	silent      bool
	failPrompts bool
	failStream  bool
	eventStatus int
	cursor      string
	prompts     int
}

func (p *promptServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result any
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/projects":
		result = api.Project{ID: "fixture"}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
		result = api.Session{ID: "sess", Status: api.SessionStatusIdle}
	case r.URL.Path == "/v1/sessions/sess":
		result = api.Session{ID: "sess", Status: api.SessionStatusIdle}
	case r.URL.Path == "/v1/sessions/sess/bootstrap":
		value := api.SessionBootstrap{Session: api.Session{ID: "sess", Status: p.status}}
		value.Transcript.Messages = append(value.Transcript.Messages, p.messages...)
		result = value
	case r.URL.Path == "/v1/sessions/sess/prompts":
		if p.failPrompts {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		p.prompts++
		if !p.silent {
			p.messages = append(p.messages, api.Message{ID: fmt.Sprintf("m%d", p.prompts), Role: api.MessageRoleAssistant})
		}
		result = api.PromptAcceptedResponse{}
	case r.URL.Path == "/v1/sessions/sess/stream":
		if p.failStream {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("data: {\"done\":true}\n"))
		return
	case r.URL.Path == "/v1/events":
		if p.eventStatus != 0 {
			w.WriteHeader(p.eventStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
		if p.cursor != "" {
			_, _ = fmt.Fprintf(w, "data: {\"event_id\":\"e\",\"cursor\":%q}\n", p.cursor)
		}
		return
	case r.URL.Path == "/v1/projects/fixture/source/workspace":
		result = api.SourceWorkspace{WorkspaceID: "ws", Roots: []api.SourceWorkspaceRoot{{ID: "root"}}}
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/presentations"):
		result = api.SourcePresentation{ID: "pres", View: api.SourceView{Tree: &api.SourceTreeView{Kind: "tree", ID: "view", State: "ready", ProjectionRevision: "rev", Extent: api.SourceViewExtent{Rows: 1, Complete: true}}}}
	case r.URL.Query().Get("limit") != "":
		rows := []api.SourceTreeRow{{Kind: "directory", Expanded: true, Address: api.SourceTreeAddress{RootID: "root", Path: "."}}}
		result = api.SourceTreeFrame{Kind: "tree", ViewID: "view", ProjectionRevision: "rev", Span: api.SourceViewSpan{End: 1}, Rows: rows}
	default:
		result = api.SourceTreeView{Kind: "tree", ID: "view", State: "ready", ProjectionRevision: "rev", Extent: api.SourceViewExtent{Rows: 1, Complete: true}}
	}
	_ = json.NewEncoder(w).Encode(result)
}

func promptState(t *testing.T, server *promptServer, cfg runConfig) *runState {
	t.Helper()
	if server.status == "" {
		server.status = api.SessionStatusIdle
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return &runState{
		cfg:       cfg,
		client:    &sidecarClient{base: httpServer.URL, http: httpServer.Client()},
		resources: &processResourceSampler{pid: os.Getpid()},
		metrics:   newMeasurements(),
	}
}

func TestPromptScenarioCountsOnlyMeasuredTurns(t *testing.T) {
	state := promptState(t, &promptServer{}, runConfig{})
	state.session.ID = "sess"
	testutil.FailErr(t, "warm prompt", state.scenarios().runPromptScenario(t.Context(), -1, false))
	if state.correctness.PromptTurnsSettled != 0 {
		t.Fatalf("warm prompt counted as a turn: %+v", state.correctness)
	}
	testutil.FailErr(t, "measured prompts", state.scenarios().runPromptScenarios(t.Context(), 2))
	if state.correctness.PromptTurnsSettled != 2 || state.correctness.StreamReplaysDone != 2 {
		t.Fatalf("measured prompts = %+v", state.correctness)
	}
}

func TestPromptScenarioReportsEachFailedStage(t *testing.T) {
	for name, server := range map[string]*promptServer{
		"accept": {failPrompts: true},
		"settle": {status: api.SessionStatusError},
		"replay": {failStream: true},
	} {
		t.Run(name, func(t *testing.T) {
			state := promptState(t, server, runConfig{})
			state.session.ID = "sess"
			if err := state.scenarios().runPromptScenario(t.Context(), 0, true); err == nil {
				t.Fatal("expected the failed stage to fail the turn")
			}
			if state.correctness.PromptTurnsSettled != 0 {
				t.Fatalf("failed turn was counted: %+v", state.correctness)
			}
		})
	}
}

func TestSidecarWorkloadRunsPromptsAndCountsEvents(t *testing.T) {
	server := &promptServer{cursor: "c1"}
	state := promptState(t, server, runConfig{iterations: 1, prompts: 2})
	testutil.FailErr(t, "sidecar workload", state.runWorkload(t.Context()))
	if server.prompts != 3 || state.correctness.PromptTurnsSettled != 2 {
		t.Fatalf("prompts = %d, correctness = %+v", server.prompts, state.correctness)
	}
	if state.correctness.EventCount != 1 {
		t.Fatalf("event count = %d", state.correctness.EventCount)
	}
}

func TestSidecarWorkloadStopsOnEachFailure(t *testing.T) {
	warm := promptState(t, &promptServer{failPrompts: true}, runConfig{iterations: 1})
	if err := warm.runWorkload(t.Context()); err == nil || !strings.Contains(err.Error(), "warm prompt") {
		t.Fatalf("warm prompt failure = %v", err)
	}
	events := promptState(t, &promptServer{eventStatus: http.StatusInternalServerError}, runConfig{iterations: 1})
	if err := events.runWorkload(t.Context()); err == nil || !strings.Contains(err.Error(), "event stream status") {
		t.Fatalf("event stream failure = %v", err)
	}
}

func TestSoakRequiresARestartCursor(t *testing.T) {
	state := promptState(t, &promptServer{}, runConfig{iterations: 1, soak: time.Second})
	if err := state.runWorkload(t.Context()); err == nil || !strings.Contains(err.Error(), "no restart cursor") {
		t.Fatalf("soak without a cursor = %v", err)
	}
}

func TestReplayUnavailableIsTyped(t *testing.T) {
	state := promptState(t, &promptServer{eventStatus: http.StatusConflict}, runConfig{})
	if _, err := state.client.startEventMonitor(t.Context(), "fixture", "old"); !errors.Is(err, errEventReplayUnavailable) {
		t.Fatalf("expired cursor = %v", err)
	}
}

func TestRunnerHelpers(t *testing.T) {
	port, err := freePort(t.Context())
	testutil.FailErr(t, "freePort", err)
	if port <= 0 {
		t.Fatalf("port = %d", port)
	}
	token, err := randomToken()
	testutil.FailErr(t, "randomToken", err)
	if len(token) != 64 {
		t.Fatalf("token length = %d", len(token))
	}
	if result := quickCheck(t.Context(), filepath.Join(t.TempDir(), "absent", "store.db")); result == "ok" {
		t.Fatal("quick check passed a store that does not exist")
	}
}

func TestSoakRunsMixedCycles(t *testing.T) {
	state := promptState(t, &promptServer{}, runConfig{iterations: 1})
	state.session.ID = "sess"
	state.project.ID = "fixture"
	testutil.FailErr(t, "soak", state.scenarios().runSoak(t.Context(), time.Millisecond))
	if state.correctness.SoakCycles == 0 {
		t.Fatal("soak completed no cycles")
	}
}
