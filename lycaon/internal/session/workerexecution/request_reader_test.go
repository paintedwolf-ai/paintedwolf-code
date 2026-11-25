package workerexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var errSessionMissing = errors.New("session missing")

type sessionMap map[string]*api.Session

func (s sessionMap) Get(_ context.Context, id string) (*api.Session, error) {
	if sess, ok := s[id]; ok {
		return sess, nil
	}
	return nil, errSessionMissing
}

func (sessionMap) CreateChild(context.Context, *api.Session, api.SpawnChildRequest) (*api.Session, error) {
	return nil, errors.New("not used")
}
func (sessionMap) Delete(context.Context, string) error { return errors.New("not used") }
func (sessionMap) UpdateSession(context.Context, string, func(*api.Session)) error {
	return errors.New("not used")
}

type fixedLimits struct{ maxSummary int }

func (l fixedLimits) Compaction(context.Context, *api.Session) compaction.CompactionConfig {
	return compaction.CompactionConfig{MaxWorkerSummaryChars: l.maxSummary}
}

// projection records each session the service asks to project.
type projection struct {
	reader   prompts.Reader
	sessions []*api.Session
	history  [][]api.Message
}

func (p *projection) project(_ context.Context, sess *api.Session, history []api.Message) (prompts.Reader, error) {
	p.sessions = append(p.sessions, sess)
	p.history = append(p.history, history)
	return p.reader, nil
}

type kickEngine struct {
	prompts.PromptTemplateEngine
	reader prompts.Reader
	name   string
	data   map[string]any
}

func (e *kickEngine) RenderKick(_ context.Context, reader prompts.Reader, name string, data map[string]any) (string, error) {
	e.reader, e.name, e.data = reader, name, data
	return "kick:" + name, nil
}

func declaredReader(t *testing.T) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1},
		prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, Declared: true})
	testutil.FailErr(t, "declared reader", err)
	return reader
}

func newReaderService(sessions sessionMap) (*Service, *projection) {
	svc := New(sessions, nil, nil, nil, nil, fixedLimits{}, nil, PeerServices{})
	proj := &projection{}
	svc.SetRequestReader(proj.project)
	return svc, proj
}

func TestWorkerRequestReaderProjectsTheChildTheSpawnWouldCreate(t *testing.T) {
	parent := &api.Session{
		ID: "parent", ProjectID: "project", Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild,
		WorkspacePath: "/work/root", WorkspaceRootID: "root-1", AgentType: "coordinator",
	}
	svc, proj := newReaderService(sessionMap{"parent": parent})
	proj.reader = declaredReader(t)

	reader, err := svc.WorkerRequestReader(t.Context(), " parent ", "  ", " explore ")
	testutil.FailErr(t, "worker request reader", err)
	if reader.Fingerprint() != proj.reader.Fingerprint() {
		t.Fatalf("reader fingerprint = %q, want the projected reader %q", reader.Fingerprint(), proj.reader.Fingerprint())
	}
	if len(proj.sessions) != 1 {
		t.Fatalf("projections = %d, want 1", len(proj.sessions))
	}
	child := proj.sessions[0]
	want := api.Session{
		ProjectID: "project", ParentSessionID: "parent", AgentType: "explore",
		Mode: api.SessionModeAsk, Posture: api.SessionPostureBuild,
		WorkspacePath: "/work/root", WorkspaceRootID: "root-1",
	}
	if child.ID != "" || child.ProjectID != want.ProjectID || child.ParentSessionID != want.ParentSessionID ||
		child.AgentType != want.AgentType || child.Mode != want.Mode || child.Posture != want.Posture ||
		child.WorkspacePath != want.WorkspacePath || child.WorkspaceRootID != want.WorkspaceRootID {
		t.Fatalf("projected child = %+v, want %+v", *child, want)
	}
	if proj.history[0] != nil {
		t.Fatalf("a new child projects with history %v, want none", proj.history[0])
	}
}

func TestWorkerRequestReaderProjectsAnExistingChildSession(t *testing.T) {
	child := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "explore"}
	svc, proj := newReaderService(sessionMap{"child": child})

	if _, err := svc.WorkerRequestReader(t.Context(), "parent", " child ", "ignored"); err != nil {
		t.Fatalf("worker request reader: %v", err)
	}
	if len(proj.sessions) != 1 || proj.sessions[0] != child {
		t.Fatalf("projected sessions = %+v, want the stored child", proj.sessions)
	}
}

func TestRequestReadersRefuseWithoutAProjectionOrSession(t *testing.T) {
	unconfigured := New(sessionMap{"s": {ID: "s"}}, nil, nil, nil, nil, fixedLimits{}, nil, PeerServices{})
	var nilService *Service
	for name, call := range map[string]func() error{
		"worker reader unconfigured": func() error {
			_, err := unconfigured.WorkerRequestReader(t.Context(), "s", "", "explore")
			return err
		},
		"next reader unconfigured": func() error {
			_, err := unconfigured.NextRequestReader(t.Context(), "s", nil)
			return err
		},
		"worker reader nil service": func() error {
			_, err := nilService.WorkerRequestReader(t.Context(), "s", "", "explore")
			return err
		},
		"next reader nil service": func() error {
			_, err := nilService.NextRequestReader(t.Context(), "s", nil)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: projected a reader without a configured projection", name)
		}
	}

	svc, proj := newReaderService(sessionMap{})
	if _, err := svc.WorkerRequestReader(t.Context(), "missing-parent", "", "explore"); !errors.Is(err, errSessionMissing) {
		t.Fatalf("missing parent: err = %v, want the session store error", err)
	}
	if _, err := svc.WorkerRequestReader(t.Context(), "parent", "missing-child", "explore"); !errors.Is(err, errSessionMissing) {
		t.Fatalf("missing child: err = %v, want the session store error", err)
	}
	if len(proj.sessions) != 0 {
		t.Fatalf("projected %d sessions after store failures, want none", len(proj.sessions))
	}
}

func TestSummaryFinalizeOptsProjectAndRenderThroughTheChildReader(t *testing.T) {
	child := &api.Session{ID: "child", AgentType: "explore"}
	svc, proj := newReaderService(sessionMap{"child": child})
	proj.reader = declaredReader(t)
	engine := &kickEngine{}
	svc.SetPromptEngine(engine)

	opts := svc.WorkerSummaryFinalizeOpts(t.Context(), child)
	if opts.MaxChars != compaction.DefaultCompactionConfig().MaxWorkerSummaryChars {
		t.Fatalf("MaxChars = %d, want the default when limits configure none", opts.MaxChars)
	}
	history := []api.Message{{Role: "assistant", Content: "done"}}
	reader, err := opts.NextRequestReader(t.Context(), "child", history)
	testutil.FailErr(t, "next request reader", err)
	if len(proj.sessions) != 1 || proj.sessions[0] != child || len(proj.history[0]) != 1 || &proj.history[0][0] != &history[0] {
		t.Fatalf("projection = %+v history %+v, want the stored child with its history", proj.sessions, proj.history)
	}

	data := map[string]any{"leg_id": "leg"}
	out, err := opts.RenderWorkerKick(t.Context(), reader, "  worker_wrap_up ", data)
	testutil.FailErr(t, "render kick", err)
	if out != "kick:worker_wrap_up" || engine.name != "worker_wrap_up" || engine.data["leg_id"] != "leg" {
		t.Fatalf("kick rendered %q with name %q data %v", out, engine.name, engine.data)
	}
	if engine.reader.Fingerprint() != proj.reader.Fingerprint() {
		t.Fatalf("kick reader fingerprint = %q, want the child's projected reader", engine.reader.Fingerprint())
	}
}

func TestRenderKickWithoutAnEngineRendersNothing(t *testing.T) {
	svc, _ := newReaderService(sessionMap{})
	out, err := svc.RenderKick(t.Context(), prompts.Reader{}, "worker_wrap_up", nil)
	if err != nil || out != "" {
		t.Fatalf("RenderKick without an engine = %q, %v; want empty", out, err)
	}
}
