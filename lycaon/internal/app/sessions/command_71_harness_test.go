package sessions

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/security"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

const command71ArtifactHandle = "33ac7986-37e4-4a06-892f-e38301bdedc3"

type command71SearchLog struct {
	mu        sync.Mutex
	discovery []map[string]int64
	content   []map[string]int64
}

func (l *command71SearchLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *command71SearchLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *command71SearchLog) WithGroup(string) slog.Handler            { return l }
func (l *command71SearchLog) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "command discovery completed" && rec.Message != "command content search completed" {
		return nil
	}
	vals := map[string]int64{}
	rec.Attrs(func(attr slog.Attr) bool {
		if attr.Value.Kind() == slog.KindInt64 {
			vals[attr.Key] = attr.Value.Int64()
		}
		return true
	})
	l.mu.Lock()
	if rec.Message == "command discovery completed" {
		if vals["entries"] > 0 {
			l.discovery = append(l.discovery, vals)
		}
	} else {
		l.content = append(l.content, vals)
	}
	l.mu.Unlock()
	return nil
}

type recordingDispatchProvider struct {
	id         string
	mu         sync.Mutex
	dispatched []modelcall.CompletionRequest
	calls      atomic.Int32
}

func (p *recordingDispatchProvider) ID() string { return p.id }
func (p *recordingDispatchProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "coord-m1"}}
}
func (p *recordingDispatchProvider) Profile() providerprofile.Profile {
	return providerprofile.Profile{UtilitySingleFlight: true}
}
func (p *recordingDispatchProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	ch, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	var text string
	for chunk := range ch {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		text += chunk.Content
	}
	return &modelcall.Completion{Content: text}, nil
}
func (p *recordingDispatchProvider) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.dispatched = append(p.dispatched, req)
	p.mu.Unlock()
	ch := make(chan modelcall.StreamChunk, 2)
	ch <- modelcall.StreamChunk{Content: "foreground-ack"}
	ch <- modelcall.StreamChunk{Done: true}
	close(ch)
	return ch, nil
}

type command71Sessions struct{ sessionID string }

func (s command71Sessions) Get(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id, OwnerPersonID: "fixture-person"}, nil
}

type command71Progress struct{ store *progress.SQLStore }

func (p command71Progress) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type command71Deliveries struct {
	mu      sync.Mutex
	records []evidence.Record
}

func (d *command71Deliveries) CommitDeliveredSource(_ context.Context, _ string, obs evidence.DeliveredSourceObservation) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, obs.Record())
	return fmt.Sprintf("read#%d", len(d.records)), nil
}

type command71Harness struct {
	database      db.Handle
	recorder      *invocation.SQLRecorder
	host          *toolhost.Runtime
	bg            *bgprocess.Registry
	procCtrl      *processcontrol.Service
	waitStore     *awaitstore.Store
	loop          *loopwake.LoopEngine
	waitReg       *tools.DefaultRegistry
	credFiles     *credentialFiles
	secRuntime    *security.Runtime
	provider      *recordingDispatchProvider
	fgClient      *llm.RoutingClient
	programClock  *programtest.ManualClock
	deliveries    *command71Deliveries
	logs          *command71SearchLog
	sessionID     string
	primaryRoot   string
	siblingLycaon string
	outputDir     string
	scratchDir    string
	seededStdout  string

	askMu       sync.Mutex
	askDecision secretmatch.Decision

	compMu      sync.Mutex
	completions chan bgprocess.Completion

	nativeMu       sync.Mutex
	onFirstNative  func()
	nativeInvokeCt atomic.Int32
}

func (*command71Harness) RecordSourceRunTerminal(context.Context, *api.Session, string, string, tools.SourceRunCapture, time.Time) {
}
func (*command71Harness) Emit(context.Context, string, anchor.ID, anchor.Envelope) {}

func newCommand71Harness(t *testing.T) *command71Harness {
	t.Helper()
	admissiontest.Enable(t)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("LYCAON_LLM_MANUAL", "")
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))

	logs := &command71SearchLog{}
	prevLog := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prevLog) })

	database := testdbfixture.Open(t, "command71.db")
	sessionID := "sess-cmd71"
	testdbseed.InsertSession(t, database, sessionID, testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, database, "run-"+sessionID, sessionID, testdbseed.DefaultProjectID)

	// Seed primary repository with pinned generated catalog and handler source, plus sibling checkout.
	primaryRoot, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval primary root", err)
	catalogBytes, err := os.ReadFile("../../toolcontract/language_catalog.generated.go")
	testutil.FailErr(t, "read language_catalog.generated.go", err)
	handlerBytes, err := os.ReadFile("../../api/sourceapi/handler.go")
	testutil.FailErr(t, "read sourceapi/handler.go", err)
	seedTree := map[string][]byte{
		"lycaon/internal/toolcontract/language_catalog.generated.go": catalogBytes,
		"lycaon/internal/api/sourceapi/handler.go":                   handlerBytes,
		"lycaon/cmd/lycaon/main.go":                                  []byte("package main\nfunc main() {}\n"),
		".env":                                                       []byte("INIT_TOKEN=cmd71-initial-secret-990011\n"),
	}
	for rel, content := range seedTree {
		abs := filepath.Join(primaryRoot, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(abs, content, 0o600))
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", primaryRoot}, args...)...)
		if out, gitErr := cmd.CombinedOutput(); gitErr != nil {
			t.Fatalf("git %v: %v\n%s", args, gitErr, string(out))
		}
	}
	runGit("init", "-q")
	runGit("add", ".")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "pinned")

	home := os.Getenv("PW_TEST_HOST_HOME")
	if home == "" {
		home, err = os.UserHomeDir()
		testutil.FailErr(t, "home dir", err)
	}
	//nolint:usetesting // Linked worktree resides outside temporary scratch locations.
	outside, err := os.MkdirTemp(home, "paintedwolf-tp13-st01-*")
	testutil.FailErr(t, "mkdir outside", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	outside, err = filepath.EvalSymlinks(outside)
	testutil.FailErr(t, "eval outside", err)
	siblingRoot := filepath.Join(outside, "st01")
	runGit("worktree", "add", "-q", "-b", "feat/st01", siblingRoot)

	releaseGrant := projectpaths.SetGrantedAccessSource(func(_, _, abs string, write bool) (projectpaths.Access, bool) {
		if write {
			return projectpaths.Access{}, false
		}
		rel, relErr := filepath.Rel(siblingRoot, abs)
		if relErr == nil && (rel == "." || filepath.IsLocal(rel)) {
			return projectpaths.Access{Path: siblingRoot, Tree: true}, true
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(releaseGrant)

	// Attach primary root + unrelated second project for credential indexing.
	queries := db.New(database)
	testutil.FailErr(t, "attach primary root", queries.CreateProjectRoot(t.Context(), db.CreateProjectRootParams{
		ID: "r1", ProjectID: testdbseed.DefaultProjectID, Path: primaryRoot, Label: "primary",
		IsPrimary: 1, AddedAt: "2026-01-01T00:00:00Z", Kind: "attached",
	}))
	testutil.FailErr(t, "insert unrelated project", queries.CreateProject(t.Context(), db.CreateProjectParams{
		ID: "proj-unrelated", Name: sql.NullString{String: "Unrelated", Valid: true},
		LastOpenedAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z",
	}))
	unrelatedRoot := t.TempDir()
	testutil.FailErr(t, "write unrelated env", os.WriteFile(filepath.Join(unrelatedRoot, ".env"), []byte("OTHER_SECRET=unrelated-secret-445566\n"), 0o600))
	testutil.FailErr(t, "attach unrelated root", queries.CreateProjectRoot(t.Context(), db.CreateProjectRootParams{
		ID: "r-unrel", ProjectID: "proj-unrelated", Path: unrelatedRoot, Label: "unrelated",
		IsPrimary: 1, AddedAt: "2026-01-01T00:00:00Z", Kind: "attached",
	}))

	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x4b}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	matcher.SetFingerprinter(fp)
	secRuntime := &security.Runtime{}
	secRuntime.InstallEvidence(t.Context(), matcher, fp)
	credFiles := &credentialFiles{
		harvest:  secRuntime.Harvest,
		fp:       fp,
		queries:  queries,
		managed:  func(string) []secretmatch.Remembered { return nil },
		exposure: func(context.Context, string, string) error { return nil },
	}
	credFiles.index = newCredentialIndex(credFiles, nil)
	credFiles.startCredentialIndex()
	t.Cleanup(func() {
		testutil.FailErr(t, "close credential index", credFiles.closeCredentialIndex(context.Background()))
	})
	secRuntime.CredentialFiles = credFiles

	h := &command71Harness{
		database:      database,
		credFiles:     credFiles,
		secRuntime:    secRuntime,
		logs:          logs,
		sessionID:     sessionID,
		primaryRoot:   primaryRoot,
		siblingLycaon: filepath.Join(siblingRoot, "lycaon"),
		outputDir:     fspath.CanonicalPath(t.TempDir()),
		scratchDir:    fspath.CanonicalPath(t.TempDir()),
		askDecision:   secretmatch.SendRedacted,
		completions:   make(chan bgprocess.Completion, 8),
	}

	// Seed 3,000-byte retained stdout artifact for handle 33ac7986-37e4-4a06-892f-e38301bdedc3.
	var artifactBuf bytes.Buffer
	for i := 1; artifactBuf.Len() < 3000; i++ {
		fmt.Fprintf(&artifactBuf, "artifact-line-%04d: retained command stdout payload segment\n", i)
	}
	h.seededStdout = artifactBuf.String()
	testutil.FailErr(t, "seed retained stdout artifact", os.WriteFile(
		filepath.Join(h.outputDir, command71ArtifactHandle+".stdout"),
		[]byte(h.seededStdout), 0o600,
	))

	// Wire real toolhost.Runtime + ProgramServices + ManualClock + bgprocess + processcontrol + wait monitor.
	cat := extpackstest.StockCatalog(t)
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: cat})
	testutil.FailErr(t, "new toolhost runtime", err)
	h.host = rt
	h.recorder = invocation.NewSQLRecorder(database)
	h.programClock = programtest.NewManualClock()
	h.deliveries = &command71Deliveries{}
	progStore := progress.NewSQLStore(database)
	testutil.FailErr(t, "seed progress", progStore.Set(sessionID, "- [ ] Replay command #71"))

	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, comp bgprocess.Completion) {
			h.completions <- comp
		},
	})
	bg.Output.SetCaptureProjector(captureprojection.New(matcher, nil))
	t.Cleanup(func() { _ = bg.Lifecycle.Close(context.Background()) })
	h.bg = bg
	rt.Commands.SetBackgroundRegistry(bg)
	testutil.FailErr(t, "set program services", rt.Commands.SetProgramServices(toolhost.ProgramServices{
		Recorder:       h.recorder,
		Blobs:          blobstore.Store{Root: t.TempDir()},
		Projector:      captureprojection.New(matcher, nil),
		Progress:       command71Progress{store: progStore},
		Sessions:       command71Sessions{sessionID: sessionID},
		Provenance:     store.NewSQL(database).Evidence,
		SourceObserver: h.deliveries,
		Clock:          h.programClock,
	}))

	// Wrap first native invocation ("head") with optional deterministic barrier.
	programs := rt.Commands.ProgramRuntime()
	baseOwners := programs.Owners
	programs.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, mem *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		owners, oErr := baseOwners(tc, paths, mem, spill)
		if oErr != nil {
			return command.ProgramOwners{}, oErr
		}
		headOp := owners.Natives["head"]
		origPrepare := headOp.Prepare
		headOp.Prepare = func(ctx context.Context, frame *projectpaths.ProgramPaths, disp commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
			prep, pErr := origPrepare(ctx, frame, disp, req)
			if pErr != nil {
				return command.NativePrepared{}, pErr
			}
			origInvoke := prep.Invoke
			prep.Invoke = func(ctx context.Context, admitted tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				h.nativeInvokeCt.Add(1)
				h.nativeMu.Lock()
				hook := h.onFirstNative
				h.nativeMu.Unlock()
				if hook != nil {
					hook()
				}
				return origInvoke(ctx, admitted, streams)
			}
			return prep, nil
		}
		owners.Natives["head"] = headOp
		return owners, nil
	}

	// Wire processcontrol + loopwake engine for background handle settlement & wait delivery.
	sqlStore := store.NewSQL(database)
	h.waitStore = &awaitstore.Store{DB: database}
	pol := &noopTurnPolicy{}
	h.procCtrl = processcontrol.New(sqlStore.Sessions, h, pol, h)
	h.procCtrl.Background = bg

	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession:       sqlStore.Sessions.Get,
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		PublishWaitLease: func(context.Context, string, loopwake.WaitLease) {},
		RunWaitResume: func(ctx context.Context, sid string, delivery loopwake.WaitDelivery) (*promptresult.Result, error) {
			if err := delivery.Admitted(); err != nil {
				return nil, err
			}
			msg := api.Message{
				ID: "resume-" + delivery.LeaseID, Role: api.MessageRoleUser,
				Kind: api.MessageKindHostLoopWake, Content: loopwake.WaitEndedText(delivery.Condition),
				CreatedAt: time.Now().UTC(),
			}
			if err := sqlStore.Transcript.AppendMessages(ctx, sid, msg); err != nil {
				return nil, err
			}
			loopwake.ObserveProcessWait(sid, delivery.Condition, h.procCtrl.Observe)
			awaitstore.EmitPhaseRecord(ctx, awaitstore.PhaseRecord{
				SessionID: sid, LeaseID: delivery.LeaseID, Handle: firstOrEmpty(delivery.Condition.Handles),
				Tool: "wait", Phase: "transcript_result_commit", Outcome: delivery.Condition.Outcome,
			})
			return &promptresult.Result{}, nil
		},
		LoopProcessDeps: loopwake.LoopProcessDeps{
			ProcessRunning: h.procCtrl.HandlesRunning,
			ProcessState:   h.procCtrl.HandleState,
			ProcessSubject: h.procCtrl.HandleSubject,
			ProcessReport:  h.procCtrl.Report,
			ObserveProcess: h.procCtrl.Observe,
		},
	})
	h.loop = loop
	h.procCtrl.SetLoop(loop.Waits, loop.Nudges)
	t.Cleanup(func() { loop.ForgetSession(context.Background(), sessionID) })

	waitReg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register wait tool", loopwake.RegisterWaitTool(waitReg, loop.Subscriptions, loopwake.WaitToolDeps{
		Store:       h.waitStore,
		WorkerKnown: func(context.Context, string, string) bool { return true },
	}))
	h.waitReg = waitReg

	// Wire normal foreground RoutingClient over ModelSecretScreen + single-flight UtilityPlane.
	screen := llm.NewModelSecretScreen(matcher, func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		h.askMu.Lock()
		dec := h.askDecision
		h.askMu.Unlock()
		return secretmatch.Resolution{Decision: dec, Guidance: "policy"}, nil
	})
	screen.SetManagedSecretEvidence(secRuntime.ModelScreeningValues)

	provider := &recordingDispatchProvider{id: "ollama-fg"}
	h.provider = provider
	provCat, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.json"))
	testutil.FailErr(t, "new provider catalog", err)
	llmReg, err := llm.NewRegistry(t.Context(), provCat, nil, nil)
	testutil.FailErr(t, "new llm registry", err)
	llmReg.SetOutboundSecretScreen(screen)
	testutil.FailErr(t, "register provider", llmReg.Register(provider))
	plane := llm.NewUtilityPlane()
	h.fgClient = llm.NewRoutingClient(llmReg, nil, nil, plane, llm.NewCapacityGate(llm.CapacityPolicy{}), nil, func(context.Context) (*llm.ModelSelection, error) {
		return &llm.ModelSelection{ProviderID: "ollama-fg", Model: "coord-m1"}, nil
	})

	return h
}

func firstOrEmpty(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}
