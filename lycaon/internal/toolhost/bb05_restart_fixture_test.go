//go:build unix

package toolhost

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
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

// M09 owner-fixture harness: composes toolhost over a persistent SQLite store.db
// so tests can close and reopen against the same database.

type bb05Sessions struct{}

func (bb05Sessions) Get(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id, OwnerPersonID: "fixture-person"}, nil
}

type bb05SourceObserver struct {
	mu      sync.Mutex
	records []evidence.Record
}

func (o *bb05SourceObserver) CommitDeliveredSource(_ context.Context, _ string, obs evidence.DeliveredSourceObservation) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, obs.Record())
	return fmt.Sprintf("read#%d", len(o.records)), nil
}

type bb05ProgressFacts struct{ store progress.Store }

func (p bb05ProgressFacts) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type bb05RestartEnv struct {
	dbPath     string
	projectDir string
	outputDir  string
	scratchDir string
	blobDir    string
	ledgerDir  string
	sessionID  string
	calls      int
}

func newBB05RestartEnv(t *testing.T, sessionID string) *bb05RestartEnv {
	t.Helper()
	admissiontest.Enable(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")

	root := t.TempDir()
	projectDir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project dir", err)
	scratchDir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve scratch dir", err)

	return &bb05RestartEnv{
		dbPath:     filepath.Join(root, "store.db"),
		projectDir: projectDir,
		outputDir:  t.TempDir(),
		scratchDir: scratchDir,
		blobDir:    t.TempDir(),
		ledgerDir:  t.TempDir(),
		sessionID:  sessionID,
	}
}

type bb05RestartHost struct {
	env        *bb05RestartEnv
	database   *db.Store
	recorder   *invocation.SQLRecorder
	runtime    *Runtime
	background *bgprocess.Registry
	ledger     *sourceledger.Store
	mutations  *projectsource.SourceMutationService
	closed     bool
}

func (e *bb05RestartEnv) boot(t *testing.T, reopen bool) *bb05RestartHost {
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
		testutil.FailErr(t, "seed progress", progStore.Set(e.sessionID, "- [ ] Run BB05 M09 restart check"))
	}

	rt, err := NewRuntime(RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "compose toolhost runtime", err)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rt.Authority.ApplyGuidanceRejects(guidance.NewStaticRejectFormatter(hints))

	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	bg.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))

	testutil.FailErr(t, "set program services", rt.Commands.SetProgramServices(ProgramServices{
		Recorder:       recorder,
		Blobs:          blobstore.Store{Root: e.blobDir},
		Projector:      captureprojection.New(&secretmatch.Matcher{}, nil),
		Progress:       bb05ProgressFacts{progStore},
		Sessions:       bb05Sessions{},
		Provenance:     store.NewSQL(database).Evidence,
		SourceObserver: &bb05SourceObserver{},
	}))
	rt.Commands.SetBackgroundRegistry(bg)

	ledger := sourceledger.New(database, e.ledgerDir, sourceledger.Timing{})
	mutations := projectsource.NewSourceMutationService(database, ledger)

	host := &bb05RestartHost{
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

func (h *bb05RestartHost) close(t *testing.T) {
	t.Helper()
	if h.closed {
		return
	}
	h.closed = true
	testutil.FailErr(t, "close mutations", h.mutations.Close())
	_ = h.background.Lifecycle.Close(context.Background())
	testutil.FailErr(t, "close store.db", h.database.Close())
}

func (h *bb05RestartHost) call(t *testing.T, ctx context.Context, source string) (string, error) {
	t.Helper()
	h.env.calls++
	callID := fmt.Sprintf("bb05-call-%d", h.env.calls)
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
	tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }
	return h.runtime.Commands.programs().Run(ctx, "command", args, tc)
}

func (h *bb05RestartHost) programCount(t *testing.T) int {
	t.Helper()
	var count int
	err := h.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_programs`).Scan(&count)
	testutil.FailErr(t, "count invocation_programs", err)
	return count
}
