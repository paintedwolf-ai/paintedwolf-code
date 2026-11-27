//go:build unix

package toolhost

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// I15 owner-fixture harness: composes toolhost (with native HTTP and mutation
// owners) over a persistent SQLite store.db for reopen and live reconnect tests.

type i15Sessions struct{}

func (i15Sessions) Get(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id, OwnerPersonID: "fixture-person"}, nil
}

type i15SourceObserver struct {
	mu      sync.Mutex
	records []evidence.Record
}

func (o *i15SourceObserver) CommitDeliveredSource(_ context.Context, _ string, obs evidence.DeliveredSourceObservation) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, obs.Record())
	return fmt.Sprintf("read#%d", len(o.records)), nil
}

type i15ProgressFacts struct{ store progress.Store }

func (p i15ProgressFacts) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type i15LoopbackGate struct{ ports []uint16 }

func (g i15LoopbackGate) Await(_ context.Context, _ tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	return tools.LoopbackConnectResult{Authorized: true, Ports: append([]uint16(nil), g.ports...)}, nil
}

func (g i15LoopbackGate) SessionLoopbackGrant(_ context.Context, _, _ string) (bool, []uint16) {
	return len(g.ports) > 0, append([]uint16(nil), g.ports...)
}

type i15Env struct {
	dbPath     string
	projectDir string
	outputDir  string
	scratchDir string
	blobDir    string
	ledgerDir  string
	sessionID  string
	ownedPorts []uint16
	calls      int
}

func newI15Env(t *testing.T, sessionID string, ownedPorts ...uint16) *i15Env {
	t.Helper()
	admissiontest.Enable(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	if len(ownedPorts) > 0 {
		confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			return ep.Host == "127.0.0.1" || ep.Host == "::1" || ep.Host == "localhost"
		})
		t.Cleanup(func() { confine.SetEgressResolver(nil) })
	}

	root := t.TempDir()
	projectDir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project dir", err)
	scratchDir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve scratch dir", err)

	return &i15Env{
		dbPath:     filepath.Join(root, "store.db"),
		projectDir: projectDir,
		outputDir:  t.TempDir(),
		scratchDir: scratchDir,
		blobDir:    t.TempDir(),
		ledgerDir:  t.TempDir(),
		sessionID:  sessionID,
		ownedPorts: append([]uint16(nil), ownedPorts...),
	}
}

type i15Host struct {
	env        *i15Env
	database   *db.Store
	recorder   *invocation.SQLRecorder
	runtime    *Runtime
	background *bgprocess.Registry
	ledger     *sourceledger.Store
	mutations  *projectsource.SourceMutationService
	closed     bool
}

func (e *i15Env) boot(t *testing.T, reopen bool) *i15Host {
	t.Helper()
	ctx := t.Context()
	database, err := db.OpenWithOptions(ctx, e.dbPath, db.UpgradeHooks{}, db.StoreOptions{FastTestSync: true})
	testutil.FailErr(t, "open store.db", err)

	if !reopen {
		testdbseed.InsertWorkflowRun(t, database, "run-"+e.sessionID, e.sessionID, testdbseed.DefaultProjectID)
		_, err = database.ExecContext(ctx, `INSERT INTO project_roots (id,project_id,path,label,is_primary,added_at) VALUES ('r1',?,?,'root',1,?)`,
			testdbseed.DefaultProjectID, e.projectDir, timelayout.Format(time.Now().UTC()))
		testutil.FailErr(t, "seed project root", err)
	}

	recorder := invocation.NewSQLRecorder(database)
	progStore := progress.NewSQLStore(database)
	if !reopen {
		testutil.FailErr(t, "seed progress", progStore.Set(e.sessionID, "- [ ] Run BB10 I15 invariant test"))
	}

	rt, err := NewRuntime(RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "compose toolhost runtime", err)
	testutil.FailErr(t, "register HTTP owner", rt.RegisterHTTP(httpaction.Deps{Boundary: rt.Boundary}))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rt.Authority.ApplyGuidanceRejects(guidance.NewStaticRejectFormatter(hints))
	if len(e.ownedPorts) > 0 {
		rt.Authority.SetSandboxLoopbackGate(i15LoopbackGate{ports: e.ownedPorts})
	}

	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	bg.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))

	testutil.FailErr(t, "set program services", rt.Commands.SetProgramServices(ProgramServices{
		Recorder:       recorder,
		Blobs:          blobstore.Store{Root: e.blobDir},
		Projector:      captureprojection.New(&secretmatch.Matcher{}, nil),
		Progress:       i15ProgressFacts{progStore},
		Sessions:       i15Sessions{},
		Provenance:     store.NewSQL(database).Evidence,
		SourceObserver: &i15SourceObserver{},
	}))
	rt.Commands.SetBackgroundRegistry(bg)

	ledger := sourceledger.New(database, e.ledgerDir, sourceledger.Timing{})
	mutations := projectsource.NewSourceMutationService(database, ledger)

	host := &i15Host{
		env:        e,
		database:   database,
		recorder:   recorder,
		runtime:    rt,
		background: bg,
		ledger:     ledger,
		mutations:  mutations,
	}
	t.Cleanup(func() { host.close(t) })
	return host
}

func (h *i15Host) close(t *testing.T) {
	t.Helper()
	if h.closed {
		return
	}
	h.closed = true
	testutil.FailErr(t, "close mutations", h.mutations.Close())
	_ = h.background.Lifecycle.Close(context.Background())
	testutil.FailErr(t, "close store.db", h.database.Close())
}

func (h *i15Host) call(t *testing.T, ctx context.Context, source string) (string, error) {
	t.Helper()
	h.env.calls++
	callID := fmt.Sprintf("i15-call-%d", h.env.calls)
	args := map[string]any{"command": source}
	contract, _ := toolcontract.Lookup("command")
	parent, err := h.recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: h.env.sessionID,
		ToolCallID: callID, ToolName: "command", Args: args, Contract: contract,
	})
	testutil.FailErr(t, "begin model receipt", err)

	tc := tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:               []projectroot.RootRef{{ID: "r1", Label: "root", Path: h.env.projectDir, IsPrimary: true}},
			ActiveRootID:        "r1",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
			SourceLedger:        h.ledger,
			SourceMutations:     h.mutations,
			History:             tools.SourceHistory{Files: h.ledger.History, Comparison: h.ledger.Comparisons, Git: h.ledger.Git, Authorship: h.ledger.Walk},
			Commands:            h.ledger.Commands,
			GitMutations:        h.ledger.Git,
			Observations:        h.ledger.Inventory,
		},
	}
	tc.Identity.Agent = "implement"
	tc.Identity.ProjectID = testdbseed.DefaultProjectID
	tc.Identity.SessionID = h.env.sessionID
	tc.Identity.ToolCallID = callID
	tc.Identity.UserTurn = 1
	tc.Invocation.ID = parent.ID
	tc.Host.SessionScratchDir = h.env.scratchDir
	tc.Host.SessionOutputDir = h.env.outputDir
	tc.Local.OwnedLoopbackPorts = append([]uint16(nil), h.env.ownedPorts...)
	tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }
	return h.runtime.Commands.programs().Run(ctx, "command", args, tc)
}

func (h *i15Host) programCount(t *testing.T) int {
	t.Helper()
	var count int
	err := h.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_programs`).Scan(&count)
	testutil.FailErr(t, "count invocation_programs", err)
	return count
}

func i15ServerPort(t *testing.T, rawURL string) uint16 {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	testutil.FailErr(t, "split server host:port", err)
	port, err := strconv.ParseUint(portText, 10, 16)
	testutil.FailErr(t, "parse server port", err)
	return uint16(port)
}
