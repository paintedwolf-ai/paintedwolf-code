// Package programtest composes the command program entry over the real
// registry, executor, SQL receipts, process registry and supervisor for tests.
package programtest

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// Options select the fixture's session, process registry and native owners.
type Options struct {
	Session     string
	Background  *bgprocess.Registry
	Natives     func(dir string) map[string]command.NativeOperation
	Input       func(dir string) commandprogram.InputRedirection
	Publication func(dir string) commandprogram.OutputPublication
	Context     func(dir string, tc *tools.ToolContext)
	Owners      func(OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error)
	Tools       []string
}

// OwnerPorts are the fixture's shared dependencies for composed owners.
type OwnerPorts struct {
	Dir      string
	Recorder *invocation.SQLRecorder
	Boundary *sandbox.Boundary
}

// Fixture is one composed program entry rooted at Dir.
type Fixture struct {
	Runtime    *command.ProgramRuntime
	Recorder   *invocation.SQLRecorder
	Database   db.Handle
	Session    string
	Dir        string
	ScratchDir string
	OutputDir  string
	progress   progress.Store
	Background *bgprocess.Registry
	Boundary   *sandbox.Boundary
	Context    func(string, *tools.ToolContext)
	Configure  func(*tools.ToolContext)
	calls      int
	mu         sync.Mutex
	delivered  []evidence.Record
}

type sessions struct {
	fixture *Fixture
}

func (sessions) Get(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id, OwnerPersonID: "fixture-person"}, nil
}

func (s sessions) CommitDeliveredSource(_ context.Context, _ string, observation evidence.DeliveredSourceObservation) (string, error) {
	s.fixture.mu.Lock()
	defer s.fixture.mu.Unlock()
	rec := observation.Record()
	s.fixture.delivered = append(s.fixture.delivered, rec)
	return fmt.Sprintf("read#%d", len(s.fixture.delivered)), nil
}

// Delivered returns all confirmed source deliveries recorded in this fixture.
func (f *Fixture) Delivered() []evidence.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]evidence.Record(nil), f.delivered...)
}

type progressFacts struct{ store progress.Store }

func (p progressFacts) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

// New composes the entry. Structured tools exist only as owner identities.
func New(t testing.TB, options Options) *Fixture {
	t.Helper()
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	dir := t.TempDir()
	database := testdbfixture.Open(t, "programs.db")
	session := options.Session
	if session == "" {
		session = "session-1"
	}
	testdbseed.InsertSession(t, database, session, testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(database)
	registry := tools.NewDefaultRegistry()
	for _, name := range append([]string{"write", "read"}, options.Tools...) {
		if _, exists := registry.Operation(name); exists {
			continue
		}
		if toolcontract.IsCommandOperation(name) {
			testutil.FailErr(t, "register "+name+" operation", registry.RegisterOperation(name))
			continue
		}
		testutil.FailErr(t, "register "+name+" owner", registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "", errors.New("structured tool is not exercised")
		}))
	}
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec)
	testutil.FailErr(t, "compile profile effects", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "fixture")
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress", owner.Set(session, "- [ ] Run the fixture program"))
	background := options.Background
	if background == nil {
		background = bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
		t.Cleanup(func() { _ = background.Lifecycle.Close(context.Background()) })
	}
	natives := map[string]command.NativeOperation{}
	if options.Natives != nil {
		natives = options.Natives(dir)
	}
	var input commandprogram.InputRedirection
	if options.Input != nil {
		input = options.Input(dir)
	}
	var publication commandprogram.OutputPublication
	if options.Publication != nil {
		publication = options.Publication(dir)
	}
	ports, err := hostprocess.New()
	testutil.FailErr(t, "host process service", err)
	scratchDir := fspath.CanonicalPath(t.TempDir())
	f := &Fixture{Recorder: recorder, Database: database, Session: session, Dir: dir, ScratchDir: scratchDir, OutputDir: t.TempDir(), Background: background, Boundary: boundary, progress: owner}
	runtime := &command.ProgramRuntime{
		Executor: executor, Registry: registry, Recorder: recorder, Blobs: blobstore.Store{Root: t.TempDir()},
		Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), Boundary: boundary,
		Progress: progressFacts{owner}, Supervisor: &command.ProgramSupervisor{Interrupter: recorder, Background: background}, Evaluate: commandprogram.Evaluate,
		Sessions: sessions{fixture: f}, Ports: ports,
		Owners: func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
			return command.ProgramOwners{Natives: natives, Input: input, Publication: func(tools.CommandOutputAdmission) commandprogram.OutputPublication { return publication }}, nil
		},
	}
	if options.Owners != nil {
		runtime.Owners = options.Owners(OwnerPorts{Dir: dir, Recorder: recorder, Boundary: boundary})
	}
	f.Runtime, f.Context = runtime, options.Context
	return f
}

// SetProgress changes the session checklist observed by reached command writes.
func (f *Fixture) SetProgress(content string) error { return f.progress.Set(f.Session, content) }

// Call opens a real model receipt for one command call and runs the entry.
func (f *Fixture) Call(t testing.TB, ctx context.Context, source string) (string, error) {
	t.Helper()
	return f.CallArgs(t, ctx, map[string]any{"command": source})
}

// CallArgs runs one command call with the model's complete arguments.
func (f *Fixture) CallArgs(t testing.TB, ctx context.Context, args map[string]any) (string, error) {
	t.Helper()
	f.calls++
	callID := fmt.Sprintf("model-call-%d", f.calls)
	contract, _ := toolcontract.Lookup("command")
	//nolint:contextcheck // The test context keeps the initiating receipt available after execution cancellation.
	parent, err := f.Recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Session, ToolCallID: callID, ToolName: "command", Args: args, Contract: contract})
	testutil.FailErr(t, "begin model receipt", err)
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: f.Dir, IsPrimary: true}}, ActiveRootID: "r1", SourceWorkspaceKind: api.SourceWorkspaceKindProject}}
	tc.Identity.Agent, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = "fixture", testdbseed.DefaultProjectID, f.Session, callID
	tc.Invocation.ID = parent.ID
	tc.Host.SessionScratchDir, tc.Host.SessionOutputDir = f.ScratchDir, f.OutputDir
	if f.Context != nil {
		f.Context(f.Dir, &tc)
	}
	if f.Configure != nil {
		f.Configure(&tc)
	}
	return f.Runtime.Run(ctx, args, tc)
}

// Programs lists program receipt states in start order.
func (f *Fixture) Programs(t *testing.T) []string {
	t.Helper()
	rows, err := f.Database.QueryContext(t.Context(), `SELECT p.state FROM invocation_programs p ORDER BY p.started_at`)
	testutil.FailErr(t, "read programs", err)
	defer func() { testutil.FailErr(t, "close program receipt rows", rows.Close()) }()
	var out []string
	for rows.Next() {
		var state string
		testutil.FailErr(t, "scan program", rows.Scan(&state))
		out = append(out, state)
	}
	testutil.FailErr(t, "finish reading program receipt rows", rows.Err())
	return out
}

// Executions maps each child operation to its settled states in order.
func (f *Fixture) Executions(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.Database.QueryContext(t.Context(), `SELECT operation_id, state FROM invocation_program_executions ORDER BY ordinal`)
	testutil.FailErr(t, "read executions", err)
	defer func() { testutil.FailErr(t, "close program receipt rows", rows.Close()) }()
	out := map[string]string{}
	for rows.Next() {
		var operation, state string
		testutil.FailErr(t, "scan execution", rows.Scan(&operation, &state))
		out[operation] += state + ";"
	}
	testutil.FailErr(t, "finish reading program receipt rows", rows.Err())
	return out
}
