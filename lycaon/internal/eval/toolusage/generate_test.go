package toolusage

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// generateSidecar answers the calls a generation run makes. Session "s1"
// settles its prompt; "s2" never finishes preparing, so its task times out.
type generateSidecar struct {
	t           *testing.T
	mu          sync.Mutex
	project     wire.CreateProjectRequest
	sessions    []wire.CreateSessionRequest
	aborted     []string
	trusted     bool
	runVersion  string
	activeReads int
	// calls orders the workflow start against the prompt it precedes.
	calls []string
}

func (f *generateSidecar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/v1/projects":
		_ = json.NewDecoder(r.Body).Decode(&f.project)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(wire.Project{ID: "project"})
	case r.Method == http.MethodPost && path == "/v1/sessions":
		var req wire.CreateSessionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.sessions = append(f.sessions, req)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(wire.Session{ID: "s" + string(rune('0'+len(f.sessions)))})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/abort"):
		f.aborted = append(f.aborted, strings.Split(path, "/")[3])
	case r.Method == http.MethodPatch && path == "/v1/projects/project/trust":
		f.trusted = true
		_ = json.NewEncoder(w).Encode(wire.ProjectTrust{})
	case r.Method == http.MethodGet && path == "/v1/workflows":
		_ = json.NewEncoder(w).Encode(wire.WorkflowListResponse{Workflows: []wire.WorkflowSummary{{ID: "implement-dispatch", Version: "2.3.4"}}})
	case r.Method == http.MethodPost && path == "/v1/sessions/s1/workflow-runs":
		var req wire.StartWorkflowRunRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.WorkflowID != "implement-dispatch" || req.WorkflowVersion != "2.3.4" {
			f.t.Errorf("workflow identity=%+v", req)
		}
		if _, err := uuid.Parse(req.OperationID); err != nil {
			f.t.Errorf("workflow operation ID: %v", err)
		}
		f.calls = append(f.calls, "workflow:"+req.Request)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(wire.WorkflowRun{ID: "run", SessionID: "s1", WorkflowID: req.WorkflowID, WorkflowVersion: func() string {
			if f.runVersion != "" {
				return f.runVersion
			}
			return req.WorkflowVersion
		}()})
	case r.Method == http.MethodPost && path == "/v1/sessions/s1/prompts":
		f.calls = append(f.calls, "prompt")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{Status: "queued", OperationID: "sub1", MessageID: "sub1"})
	case strings.HasSuffix(path, "/checkpoints"):
		_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{Checkpoints: []wire.CheckpointEvent{}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/workflow-runs/active"):
		f.activeReads++
		_ = json.NewEncoder(w).Encode(wire.ActiveWorkflowRunResponse{})
	case path == "/v1/sessions/s1/messages":
		_ = json.NewEncoder(w).Encode(wire.SessionTranscriptPage{Messages: []wire.Message{
			{ID: "sub1", Role: wire.MessageRoleUser, Content: "explain"}, {Role: wire.MessageRoleAssistant, Content: "Explained."},
		}})
	case path == "/v1/sessions/s1":
		_ = json.NewEncoder(w).Encode(wire.Session{ID: "s1", Status: wire.SessionStatusIdle})
	case path == "/v1/sessions/s2":
		_ = json.NewEncoder(w).Encode(wire.Session{ID: "s2", Status: wire.SessionStatusPreparing})
	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestRunGenerateRecordsEveryTaskAndContinuesPastATimeout(t *testing.T) {
	fake := &generateSidecar{t: t}
	server := httptest.NewServer(fake)
	defer server.Close()
	tasks := []GenerateTask{
		{CorpusTask: CorpusTask{ID: "t1", Prompt: "explain"}, Posture: wire.SessionPostureVet, ProviderID: "fixture", Model: "qwen", Workflow: "implement-dispatch", WorkflowVersion: "2.3.4", Meta: json.RawMessage(`{"repo":"flask"}`)},
		{CorpusTask: CorpusTask{ID: "t2", Prompt: "fix"}},
	}
	var manifest bytes.Buffer
	err := RunGenerate(t.Context(), GenerateOptions{
		BaseURL: server.URL, Token: "fixture", ProjectDir: "/work/flask", ProjectName: "flask", Tasks: tasks,
		Timeout: 300 * time.Millisecond, Manifest: &manifest,
		Unattended: UnattendedPolicy{ApprovalGuidance: "Continue without it.", Answer: "Use your best judgment.", MaxInterventions: 3},
	})
	testutil.FailErr(t, "run generate", err)

	if fake.project.Name == nil || *fake.project.Name != "flask" {
		t.Fatalf("project request = %+v, want the name flask", fake.project)
	}
	if len(fake.sessions) != 2 || fake.sessions[0].Posture != wire.SessionPostureVet || fake.sessions[0].Model != "qwen" || fake.sessions[1].Posture != wire.SessionPostureBuild {
		t.Fatalf("session requests = %+v", fake.sessions)
	}
	if !fake.trusted || fake.activeReads == 0 || strings.Join(fake.calls, ",") != "workflow:explain" {
		t.Fatalf("trusted = %v, active reads = %d, calls = %v, want the exact workflow start and settled active-run read", fake.trusted, fake.activeReads, fake.calls)
	}
	var entries []ManifestEntry
	for _, line := range bytes.Split(bytes.TrimSpace(manifest.Bytes()), []byte("\n")) {
		var entry ManifestEntry
		testutil.FailErr(t, "decode manifest", json.Unmarshal(line, &entry))
		entries = append(entries, entry)
	}
	if len(entries) != 2 || entries[0].Status != GenerateSettled || entries[0].RootSession != "s1" || entries[0].WorkflowID != "implement-dispatch" || entries[0].WorkflowVersion != "2.3.4" || string(entries[0].Meta) != `{"repo":"flask"}` {
		t.Fatalf("manifest = %+v", entries)
	}
	if entries[1].Status != GenerateTimeout || len(fake.aborted) != 1 || fake.aborted[0] != "s2" {
		t.Fatalf("timed-out task = %+v, aborted %v", entries[1], fake.aborted)
	}
}

func TestRunGenerateRequiresATimeoutAndManifest(t *testing.T) {
	policy := UnattendedPolicy{ApprovalGuidance: "no", Answer: "yes", MaxInterventions: 1}
	if err := RunGenerate(t.Context(), GenerateOptions{Unattended: policy, Manifest: &bytes.Buffer{}}); err == nil {
		t.Fatal("a run without a timeout started")
	}
	if err := RunGenerate(t.Context(), GenerateOptions{Unattended: policy, Timeout: time.Second}); err == nil {
		t.Fatal("a run without a manifest started")
	}
}
