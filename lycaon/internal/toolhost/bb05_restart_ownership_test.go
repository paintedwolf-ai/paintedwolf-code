//go:build unix

package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/programtest"
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
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	observer   *bb05SourceObserver
	reviewHook tools.FileChangeReviewer
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
	obs := &bb05SourceObserver{}

	testutil.FailErr(t, "set program services", rt.Commands.SetProgramServices(ProgramServices{
		Recorder:       recorder,
		Blobs:          blobstore.Store{Root: e.blobDir},
		Projector:      captureprojection.New(&secretmatch.Matcher{}, nil),
		Progress:       bb05ProgressFacts{progStore},
		Sessions:       bb05Sessions{},
		Provenance:     store.NewSQL(database).Evidence,
		SourceObserver: obs,
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
		observer:   obs,
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
	if h.reviewHook != nil {
		tc.Files.FileChangeReview = h.reviewHook
	} else {
		tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }
	}
	return h.runtime.Commands.programs().Run(ctx, "command", args, tc)
}

func (h *bb05RestartHost) programCount(t *testing.T) int {
	t.Helper()
	var count int
	err := h.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_programs`).Scan(&count)
	testutil.FailErr(t, "count invocation_programs", err)
	return count
}

// M09: Fixture-controlled precommit failure followed by restart against the same store.db.
func TestBB05_M09_RestartAfterPrecommitFailurePreservesBaselineAndRunsFreshCommand(t *testing.T) {
	ctx := t.Context()
	env := newBB05RestartEnv(t, "bb05-m09-precommit")
	target := filepath.Join(env.projectDir, "target.txt")
	testutil.FailErr(t, "write baseline target", os.WriteFile(target, []byte("baseline\n"), 0o600))

	host1 := env.boot(t, false)
	// Producer emits bytes then exits nonzero, forcing redirect Abandon before commit.
	raw, err := host1.call(t, ctx, "(printf 'mutated\\n'; false) > target.txt")
	testutil.FailErr(t, "run precommit-abandoned program", err)
	outcome := decodeProgramOutcome(t, raw)
	if outcome.Status == 0 || outcome.CompletedEffects != 0 {
		t.Fatalf("precommit failure outcome = %+v, want nonzero status and 0 completed effects", outcome)
	}
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read target after precommit abandon", err)
	if string(body) != "baseline\n" {
		t.Fatalf("precommit failure modified target: %q", body)
	}
	if host1.programCount(t) != 1 {
		t.Fatalf("expected 1 program before restart, got %d", host1.programCount(t))
	}

	// Restart against the same SQLite store.db.
	host1.close(t)
	host2 := env.boot(t, true)

	reloaded, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, outcome.ProgramID)
	testutil.FailErr(t, "fresh read of precommit-failed program after restart", err)
	var recOutcome command.ProgramOutcome
	testutil.FailErr(t, "decode reloaded outcome", json.Unmarshal([]byte(reloaded), &recOutcome))
	if recOutcome.ProgramID != outcome.ProgramID || recOutcome.Status == 0 || recOutcome.CompletedEffects != 0 {
		t.Fatalf("reloaded precommit outcome = %+v", recOutcome)
	}
	if host2.programCount(t) != 1 {
		t.Fatalf("restart replayed precommit-failed program: count=%d", host2.programCount(t))
	}

	// Fresh command on restarted host succeeds and updates target.txt.
	nextRaw, err := host2.call(t, ctx, "printf 'after-restart\\n' > target.txt")
	testutil.FailErr(t, "run new command after restart", err)
	nextOutcome := decodeProgramOutcome(t, nextRaw)
	if nextOutcome.State != "completed" || nextOutcome.Status != 0 {
		t.Fatalf("post-restart command outcome = %+v", nextOutcome)
	}
	body, err = os.ReadFile(target)
	testutil.FailErr(t, "read updated target", err)
	if string(body) != "after-restart\n" {
		t.Fatalf("target after fresh command = %q", body)
	}
}

// M09: Committed effect before result delivery, then restart against the same store.db.
func TestBB05_M09_RestartAfterCommittedEffectBeforeResultDelivery(t *testing.T) {
	ctx := t.Context()
	env := newBB05RestartEnv(t, "bb05-m09-delivery")
	target := filepath.Join(env.projectDir, "committed.txt")
	testutil.FailErr(t, "write initial target", os.WriteFile(target, []byte("initial\n"), 0o600))

	host1 := env.boot(t, false)
	_, err := host1.database.ExecContext(ctx, `CREATE TRIGGER bb05_abort_delivery BEFORE UPDATE ON invocation_program_publications
		WHEN NEW.state = 'committed'
		BEGIN SELECT RAISE(ABORT, 'delivery interrupted after commit'); END`)
	testutil.FailErr(t, "install delivery failure trigger", err)

	res, err := host1.call(t, ctx, "printf 'committed-bytes\\n' > committed.txt && cat committed.txt")
	if err == nil || res != "" {
		t.Fatalf("expected delivery failure, got res=%q err=%v", res, err)
	}

	// Committed bytes already landed on disk before delivery failed.
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read committed target", err)
	if string(body) != "committed-bytes\n" {
		t.Fatalf("committed bytes lost: %q", body)
	}

	var programID string
	err = host1.database.QueryRowContext(ctx, `SELECT id FROM invocation_programs`).Scan(&programID)
	testutil.FailErr(t, "query program id", err)
	if pendingRes, pendingErr := host1.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, programID); pendingErr == nil || pendingRes != "" {
		t.Fatalf("pending publication reported completed result: %q / %v", pendingRes, pendingErr)
	}

	// Drop failure trigger, shut down host1, and restart host2 on the same store.db.
	_, err = host1.database.ExecContext(ctx, `DROP TRIGGER bb05_abort_delivery`)
	testutil.FailErr(t, "drop delivery failure trigger", err)
	host1.close(t)

	host2 := env.boot(t, true)
	resolveOutputDir := func(_ context.Context, sessionID string) (string, error) {
		if sessionID != env.sessionID {
			return "", fmt.Errorf("unexpected session %q", sessionID)
		}
		return env.outputDir, nil
	}
	testutil.FailErr(t, "recover publications after restart", host2.runtime.Commands.RecoverProgramPublications(ctx, resolveOutputDir))
	// Recovery must be idempotent and never rerun the effect.
	testutil.FailErr(t, "repeat recovery", host2.runtime.Commands.RecoverProgramPublications(ctx, resolveOutputDir))

	if host2.programCount(t) != 1 {
		t.Fatalf("recovery replayed execution: count=%d", host2.programCount(t))
	}
	body, err = os.ReadFile(target)
	testutil.FailErr(t, "read target after restart recovery", err)
	if string(body) != "committed-bytes\n" {
		t.Fatalf("earlier committed bytes changed on recovery: %q", body)
	}

	reloadedRaw, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, programID)
	testutil.FailErr(t, "read recovered receipt after restart", err)
	var reloaded command.ProgramOutcome
	testutil.FailErr(t, "decode recovered receipt", json.Unmarshal([]byte(reloadedRaw), &reloaded))
	if reloaded.ProgramID != programID || reloaded.State != "completed" || len(reloaded.Outputs) != 1 {
		t.Fatalf("recovered receipt = %+v", reloaded)
	}

	// Verify a fresh command succeeds on the restarted host.
	freshRaw, err := host2.call(t, ctx, "cat committed.txt")
	testutil.FailErr(t, "run new command on restarted host", err)
	fresh := decodeProgramOutcome(t, freshRaw)
	if fresh.State != "completed" || fresh.Status != 0 || fresh.Stdout != "committed-bytes\n" {
		t.Fatalf("fresh command after restart = %+v", fresh)
	}
}

// M09: Reconnect without restart (separate scenario) and restart after parked work.
func TestBB05_M09_ReconnectWithoutRestartAndRestartAfterParkedWork(t *testing.T) {
	t.Run("reconnect_without_restart", func(t *testing.T) {
		ctx := t.Context()
		env := newBB05RestartEnv(t, "bb05-m09-reconnect")
		host := env.boot(t, false)

		raw, err := host.call(t, ctx, "printf 'once\\n' > log.txt")
		testutil.FailErr(t, "run initial write", err)
		first := decodeProgramOutcome(t, raw)
		if first.State != "completed" || first.Status != 0 {
			t.Fatalf("initial write = %+v", first)
		}

		// Simulate repeated client reconnects reading durable receipts without restarting host.
		for i := range 3 {
			receiptRaw, err := host.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, first.ProgramID)
			testutil.FailErr(t, fmt.Sprintf("reconnect read %d", i), err)
			var receipt command.ProgramOutcome
			testutil.FailErr(t, "decode reconnect receipt", json.Unmarshal([]byte(receiptRaw), &receipt))
			if receipt.ProgramID != first.ProgramID || receipt.State != "completed" || receipt.Status != 0 {
				t.Fatalf("reconnect receipt %d = %+v", i, receipt)
			}
		}
		if host.programCount(t) != 1 {
			t.Fatalf("reconnect replayed effect: program count=%d", host.programCount(t))
		}
		body, err := os.ReadFile(filepath.Join(env.projectDir, "log.txt"))
		testutil.FailErr(t, "read log.txt after reconnect", err)
		if string(body) != "once\n" {
			t.Fatalf("log.txt mutated on reconnect: %q", body)
		}

		nextRaw, err := host.call(t, ctx, "cat log.txt")
		testutil.FailErr(t, "run follow-up command after reconnect", err)
		next := decodeProgramOutcome(t, nextRaw)
		if next.State != "completed" || next.Stdout != "once\n" {
			t.Fatalf("follow-up after reconnect = %+v", next)
		}
	})

	t.Run("restart_after_parked_work", func(t *testing.T) {
		ctx := t.Context()
		env := newBB05RestartEnv(t, "bb05-m09-parked")
		host1 := env.boot(t, false)

		clock := programtest.NewManualClock()
		waiting := programtest.NewWaitingProgram(clock, programtest.SleepState(600*time.Second))
		parkedCh := make(chan *waitStatePark, 1)
		host1.runtime.Commands.SetProgramParking(waitStateParking{parked: parkedCh})
		host1.runtime.Commands.programs().Clock = clock
		host1.runtime.Commands.programs().Evaluate = waiting.Evaluate

		raw, err := host1.call(t, ctx, "sleep 600")
		testutil.FailErr(t, "park long wait", err)
		var parked command.ParkedProgramResult
		testutil.FailErr(t, "decode parked result", json.Unmarshal([]byte(raw), &parked))
		if parked.State != "parked" || parked.ProgramID == "" {
			t.Fatalf("expected parked program, got %+v", parked)
		}
		recPark := <-parkedCh

		// Simulate host crash/restart: reconcile running receipts on host2 without replaying.
		waiting.EndWait()
		<-recPark.settled
		host1.close(t)

		host2 := env.boot(t, true)
		// Also test InterruptRunning against an in-flight running row to verify lost outcome.
		_, err = host2.database.ExecContext(ctx, `UPDATE invocation_programs SET state='running', status_code=NULL, stop_code='', settled_at=NULL WHERE id=?`, parked.ProgramID)
		testutil.FailErr(t, "pin running state across crash", err)

		interrupted, err := host2.recorder.InterruptRunning(ctx)
		testutil.FailErr(t, "reconcile running programs on restart", err)
		if interrupted < 1 {
			t.Fatalf("expected >=1 interrupted receipt on restart, got %d", interrupted)
		}
		lostRaw, err := host2.runtime.Commands.ProgramReceiptResult(ctx, env.sessionID, parked.ProgramID)
		testutil.FailErr(t, "read lost parked program after restart", err)
		var lost command.ProgramOutcome
		testutil.FailErr(t, "decode lost outcome", json.Unmarshal([]byte(lostRaw), &lost))
		if lost.ProgramID != parked.ProgramID || lost.State != "interrupted" || lost.Status != 130 || lost.StopCode != toolrejection.ToolOwnerInterruptedCode {
			t.Fatalf("lost parked program after restart = %+v", lost)
		}
		if host2.programCount(t) != 1 {
			t.Fatalf("restart replayed parked program: count=%d", host2.programCount(t))
		}

		freshRaw, err := host2.call(t, ctx, "echo recovered-after-park")
		testutil.FailErr(t, "run new command after parked restart", err)
		fresh := decodeProgramOutcome(t, freshRaw)
		if fresh.State != "completed" || fresh.Status != 0 || fresh.Stdout != "recovered-after-park\n" {
			t.Fatalf("new command after parked restart = %+v", fresh)
		}
	})
}

// L03b: Verify socks_tcp lease attribution lifecycle in confine.
// When a sandboxed command's ActionLease closes, EgressBound flips false while
// CommandForLineage retains the retired lineage for project successor fallback.
// A surviving grandchild dialing over socks_tcp with no live project successor
// is lawfully refused as unleased rather than leaking unattributed egress.
func TestBB05_L03b_SocksTCPLeaseAttributionAudit(t *testing.T) {
	testutil.RequireLineage(t, lineage.Supported())
	addrs, err := confine.StartEgressBroker(t.TempDir(), net.Listen)
	testutil.FailErr(t, "start egress broker", err)
	t.Cleanup(func() { _ = confine.StopEgressBroker() })
	if addrs.SOCKS == "" {
		t.Fatal("egress broker started without SOCKS listener")
	}

	c := &confine.Confinement{
		Network:       confine.NetworkProxyOnly,
		SocksProxyEnv: true,
		ProjectID:     "bb05-l03b-project",
	}
	lease, err := confine.BindAction(c, confine.EgressCommand{
		ProjectID:   "bb05-l03b-project",
		SessionID:   "bb05-l03b-session",
		ToolCallID:  "bb05-l03b-call",
		ToolName:    "command",
		CommandLine: "grandchild-socks-audit",
	})
	testutil.FailErr(t, "bind action lease", err)
	t.Cleanup(func() { lease.Close(context.Background()) })

	if !confine.EgressBound(c) {
		t.Fatal("live action lease must be EgressBound")
	}
	envJoined := strings.Join(confine.ProxyEnv(*c), "\n")
	if !strings.Contains(envJoined, "ALL_PROXY=socks5h://"+addrs.SOCKS) {
		t.Fatalf("ProxyEnv missing socks5h ALL_PROXY: %s", envJoined)
	}

	lineageID := lease.LineageID()
	if lineageID == "" {
		t.Fatal("bound action missing lineage ID")
	}
	lease.Close(t.Context())

	if confine.EgressBound(c) {
		t.Fatal("closed action lease remained EgressBound")
	}
	retired, ok := confine.CommandForLineage(lineageID)
	if !ok || retired.ProjectID != "bb05-l03b-project" {
		t.Fatalf("retired lineage metadata = (%+v, %v), want retained project metadata", retired, ok)
	}
}
