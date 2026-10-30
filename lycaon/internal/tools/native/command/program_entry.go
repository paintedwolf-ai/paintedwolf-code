package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// ProgramRuntime holds the host services every command program composes.
// It is wired once; per-call state lives in programRun.
type ProgramRuntime struct {
	Executor   *toolexecution.Executor
	Registry   *tools.DefaultRegistry
	Recorder   *invocation.SQLRecorder
	Blobs      blobstore.Store
	Projector  *captureprojection.Projector
	Progress   toolexecution.ProgramProgressFacts
	Supervisor *ProgramSupervisor
	// Evaluate is the statement scheduler, commandprogram.Evaluate in production.
	Evaluate func(context.Context, commandsurface.ProgramSnapshot, commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error)
	Boundary *sandbox.Boundary
	// Sessions names the person who started the session; the program acts for them.
	Sessions SessionOwners
	// SourceObserver confirms delivered source windows into the evidence ledger.
	SourceObserver invocation.ProgramSourceObserver
	// Builtins supplies host utilities for one invocation; nested runs their command operands.
	Builtins func(tools.ToolContext, commandprogram.NestedCommand, NativeBindings) map[string]commandprogram.OperationDispatcher
	// Owners composes one program's host owner set from its root directory frame.
	Owners func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (ProgramOwners, error)
	// Ports reads host listeners and their holders for lsof.
	Ports PortInventory
	// ExternalHidden refuses the explicit `external --` selector; native gap
	// launches still run and are recorded.
	ExternalHidden bool
	// Parking parks a call whose program waits; nil keeps every program in
	// the foreground.
	Parking tools.ProgramParking
	// ParkThreshold is how long a call holds its turn while its program waits;
	// zero means DefaultParkThreshold.
	ParkThreshold time.Duration
	// Clock times program waits; nil is the system clock.
	Clock commandprogram.Clock
	// SourceRuns records a parked program's run evidence.
	SourceRuns SourceRunRecorder

	mu        sync.Mutex
	outputs   map[string]*invocation.ProgramOutputRetention
	resources map[string]*sessionOutputResources
	disabled  atomic.Bool
}

// SetDisabled turns the command program control off or on. Turning it off
// refuses new programs and stops every supervised program and its owned jobs,
// which settle with their actual outcomes; nothing is rerouted to a shell.
// It returns the programs whose stop was requested.
func (r *ProgramRuntime) SetDisabled(disabled bool) []string {
	r.disabled.Store(disabled)
	if !disabled || r.Supervisor == nil {
		return nil
	}
	return r.Supervisor.StopAll()
}

func (r *ProgramRuntime) clock() commandprogram.Clock {
	if r.Clock == nil {
		return commandprogram.SystemClock()
	}
	return r.Clock
}

func (r *ProgramRuntime) parkThreshold() time.Duration {
	if r.ParkThreshold <= 0 {
		return DefaultParkThreshold
	}
	return r.ParkThreshold

}

// SessionOwners reads the session whose owner a program acts for.
type SessionOwners interface {
	Get(context.Context, string) (*api.Session, error)
}

// programRun is one invocation's frozen frame, surface and ledger bindings.
type programRun struct {
	control         programControlDefinition
	publicationOnce sync.Once
	publicationErr  error
	publishDocument func(context.Context, tools.ToolContext, *toolcontract.ProgramMemory, tools.EditorDocumentPublication) (tools.EditorDocumentApplied, error)

	externalHidden bool
	// retainOutput keeps one child stream in the program output store.
	retainOutput  func(ctx context.Context, executionID, stream string, data []byte, complete bool) error
	executor      *toolexecution.Executor
	registry      *tools.DefaultRegistry
	recorder      *invocation.SQLRecorder
	supervisor    *ProgramSupervisor
	progress      toolexecution.ProgramProgressFacts
	frame         toolexecution.ProgramFrame
	surface       toolexecution.ProgramSurface
	memory        *toolcontract.ProgramMemory
	capture       invocation.ProgramInputCapture
	tc            tools.ToolContext
	root          toolcontract.OperationInvocation
	parentReceipt string
	source        string
	sourcesMu     sync.RWMutex
	sources       map[string]string
	catalog       toolcontract.LanguageCatalog
	commands      map[string]toolcontract.CommandSpecification
	nativeDefs    map[string]toolcontract.OperationDefinition
	baseEnv       []string

	// envelope carries the call's launch capability requests to each launch.
	envelope map[string]any
}

// ProgramOutcome is the command tool's result for a settled program.
type ProgramOutcome struct {
	ProgramID        string                           `json:"program_id"`
	State            string                           `json:"state"`
	Status           int                              `json:"status"`
	StopCode         string                           `json:"stop_code,omitempty"`
	Stdout           string                           `json:"stdout"`
	Stderr           string                           `json:"stderr"`
	StdoutRef        string                           `json:"stdout_ref,omitempty"`
	StderrRef        string                           `json:"stderr_ref,omitempty"`
	Outputs          []api.CommandProgramOutputWindow `json:"outputs,omitempty"`
	Truncated        bool                             `json:"truncated,omitempty"`
	ExecutedChildren int                              `json:"executed_children"`
	CompletedEffects int                              `json:"completed_effects"`
	UncertainEffects int                              `json:"uncertain_effects"`
	NativeGaps       int                              `json:"native_gaps,omitempty"`
	// CPUTimeUnmeasured marks a time report whose user and sys count native
	// operations as zero.
	CPUTimeUnmeasured bool `json:"cpu_time_unmeasured,omitempty"`
	ReplayAvailable   bool `json:"replay_available"`
	// Refusal is the host decision that stopped the program; the output and
	// status of every child that ran before it stay in this outcome.
	Refusal *ProgramRefusal `json:"refusal,omitempty"`
}

// ProgramRefusal is a typed host refusal at the program's stop point with its
// rendered feedback.
type ProgramRefusal struct {
	Code            string                                       `json:"code"`
	Feedback        string                                       `json:"feedback"`
	Details         map[string]any                               `json:"details,omitempty"`
	DeliveryFailure *toolexecution.ProgramRefusalDeliveryFailure `json:"delivery_failure,omitempty"`
}

// Run parses and validates the whole submitted program before any effect,
// freezes frame and surface, records the program, evaluates it under
// supervision, settles it and returns the bounded owner projection.
func (r *ProgramRuntime) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	ctx = sandbox.WithSessionID(ctx, tc.Identity.SessionID)
	source, _ := args["command"].(string)
	if source == "" {
		return "", toolrejection.RejectInvalidArguments("COMMAND_ARGV_REQUIRED", map[string]any{"field": "command"})
	}
	if r != nil && r.disabled.Load() {
		return "", toolrejection.RejectInvalidArguments("COMMAND_PROGRAMS_DISABLED", map[string]any{"tool": "command"})
	}
	if r == nil || r.Executor == nil || r.Recorder == nil || r.Evaluate == nil || r.Supervisor == nil || r.Sessions == nil || r.Projector == nil {
		return "", fmt.Errorf("command program runtime is not configured")
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return "", err
	}
	callCtx, call := ctx, r.newProgramCall(ctx, args, catalog)
	ctx = call.ctx
	handedOff := false
	defer func() {
		if !handedOff {
			call.detach()
			call.end()
		}
	}()
	commands, err := compiledCommandIndex()
	if err != nil {
		return "", err
	}
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		return "", err
	}
	memory, err := toolcontract.NewProgramMemory(limits)
	if err != nil {
		return "", err
	}
	parsed, snapshot, err := parseProgram(ctx, memory, source)
	if err != nil {
		return "", err
	}
	// The plan and frame stay alive while & jobs still run under supervision;
	// they are released when the program settles.
	release := func() { _ = snapshot.Close(); _ = parsed.Close() }
	defer func() {
		if !handedOff {
			release()
		}
	}()

	run := &programRun{catalog: catalog, commands: commands, envelope: launchEnvelope(args), tc: tc, memory: memory, source: source, parentReceipt: tc.Invocation.ID}
	paths, err := r.bindProgramFrame(ctx, run, args, snapshot.Plan.CatalogDigest)
	if err != nil {
		return "", err
	}
	releasePlan := release
	release = func() { run.frame.Close(); releasePlan() }
	if strings.TrimSpace(tc.Host.SessionOutputDir) == "" {
		return "", toolrejection.RejectInvalidArguments(tools.SessionOutputUnavailableCode, map[string]any{"path": "@output"})
	}
	documents, err := newProgramDocuments(ctx, tc, memory)
	if err != nil {
		return "", err
	}
	releaseDocuments := release
	release = func() { documents.close(); releaseDocuments() }
	ctx = context.WithValue(ctx, programDocumentsKey{}, documents)
	ctx = tools.WithCommandReadObserver(ctx, documents.read)
	// A validation run's source identity is captured before anything runs.
	var verification func() commandSourceIdentity
	if tc.Execution.VerificationCheck {
		_, reader := commandSourceReaders(ctx, tc, paths.Cwd().Abs)
		identity := reader()
		verification = func() commandSourceIdentity { return identity }
	}
	var owners ProgramOwners
	if r.Owners != nil {
		if owners, err = r.Owners(tc, paths, memory, r.sessionResources(tc.Identity.SessionID)); err != nil {
			return "", err
		}
	}
	run.executor, run.registry, run.recorder, run.supervisor, run.progress = r.Executor, r.Registry, r.Recorder, r.Supervisor, r.Progress
	run.externalHidden = r.ExternalHidden
	run.publishDocument = owners.PublishDocument
	if err := r.bindProgramCapture(ctx, run); err != nil {
		return "", err
	}
	outputs, scope, screening := run.capture.Outputs, run.capture.Scope, run.capture.Screening
	jobs := r.Supervisor.Jobs(tc.Identity.SessionID, tc.Identity.ProjectID)
	ctx, err = jobs.BeginProgram(ctx)
	if err != nil {
		return "", err
	}
	dispatcher, publication, releaseSurface, err := r.bindProgramSurface(ctx, run, owners, paths)
	if err != nil {
		return "", err
	}
	if releaseSurface != nil {
		releaseBefore := release
		release = func() { releaseSurface(); releaseBefore() }
	}

	if err := r.beginProgram(ctx, run, outputs, snapshot.Plan, scope, screening); err != nil {
		return "", err
	}
	if tc.Effects.ReportProgress != nil {
		ctx = invocation.WithActivityObserver(ctx, func(ctx context.Context, program string) {
			if receipt, err := r.Recorder.ActivitySummary(ctx, tc.Identity.SessionID, program); err == nil {
				tc.Effects.ReportProgress(api.ToolProgress{Phase: "executing", CommandProgram: &receipt})
			}
		})
	}
	stdout, stderr := r.newProgramStreams(ctx, run)
	releaseOutputs := release
	release = func() { _ = stdout.Close(); _ = stderr.Close(); releaseOutputs() }
	run.baseEnv, err = programEnvironment(tc)
	if err != nil {
		return "", err
	}
	sources := owners.Sources
	if sources == nil {
		if sc, ok := owners.Input.(commandprogram.SourceCapture); ok {
			sources = sc
		}
	}
	ports := commandprogram.EvaluationPorts{
		Invocation: run.root, Dispatch: dispatcher, Refusals: dispatcher.reportRefusal, Binder: commandprogram.CatalogBinder{}, Files: projectpaths.GlobFiles{Paths: paths},
		Publication: publication, Input: owners.Input, Sources: sources,
		Executions: dispatcher, Supervision: r.Supervisor, Predicates: commandprogram.PredicateRuntime{Patterns: commandprogram.NativePredicatePatterns{Memory: memory}},
		Memory:         memory,
		Clock:          r.clock(),
		ResourceLimits: confineLimits{},
		Environment:    run.baseEnv,
		ScratchDir:     tc.Host.SessionScratchDir,
		Streams:        commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr},
	}
	// Evaluation writes the call's effects privately until its result belongs
	// to the call; a parked program's later effects never reach the call.
	callTC := tc
	run.tc.Effects.Out = &tools.ToolInvocationOut{}
	park := newProgramPark(r.clock(), call, r.parkThreshold())
	ports.Waits = park
	handedOff = true
	return r.attend(callCtx, &programEvaluation{
		run: run, call: call, park: park, plan: snapshot.Plan, ports: ports, dispatcher: dispatcher,
		stdout: stdout, stderr: stderr, outputs: outputs, scope: scope, documents: documents,
		paths: paths, verification: verification, release: release, callTC: callTC,
	})

}

// bindProgramFrame resolves addressed roots and the session owner before
// freezing the initiating model call and its execution identity.
func (r *ProgramRuntime) bindProgramFrame(ctx context.Context, run *programRun, args map[string]any, catalogDigest string) (*projectpaths.ProgramPaths, error) {
	tc := run.tc
	cwd, _ := args["cwd"].(string)
	paths, err := projectpaths.NewProgramPaths(ctx, r.Boundary, tc, cwd)
	if err != nil {
		return nil, err
	}
	session, err := r.Sessions.Get(ctx, tc.Identity.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.OwnerPersonID == "" {
		return nil, fmt.Errorf("command program requires the session owner")
	}
	branch, err := tc.SourceBranch(tc.Source.ActiveRootID)
	if err != nil {
		return nil, err
	}
	root := toolcontract.OperationInvocation{
		Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: uuid.NewString(), NodeID: "program", ExecutionID: uuid.NewString()},
		ActorID: session.OwnerPersonID, SessionID: tc.Identity.SessionID,
		Scope:        toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: tc.Identity.ProjectID, RootID: tc.Source.ActiveRootID, BranchID: branch.String(), Directory: cwd},
		SourceUnitID: "submitted", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: catalogDigest,
	}
	frame, err := toolexecution.FreezeProgramFrame(ctx, root, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: args}, run.memory)
	if err != nil {
		return nil, err
	}
	run.root, run.frame = root, frame
	return paths, nil
}

// bindProgramCapture binds source and stream retention to the same program,
// memory pool and secret-screening scope.
func (r *ProgramRuntime) bindProgramCapture(ctx context.Context, run *programRun) error {
	tc, memory, root := run.tc, run.memory, run.root
	outputs, err := r.sessionOutputs(ctx, tc.Identity.SessionID)
	if err != nil {
		return err
	}
	scope := captureprojection.Scope{ProjectID: tc.Identity.ProjectID, RootSessionID: tc.ChatSessionID(), SessionID: tc.Identity.SessionID}
	screening := captureprojection.StreamOptions{WorkingMemory: memory, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}
	run.retainOutput = func(ctx context.Context, executionID, stream string, data []byte, complete bool) error {
		spill, redacted, err := r.retainScreened(ctx, memory, executionID+":"+stream, data, scope, screening)
		if err != nil {
			return err
		}
		output := invocation.RetainedOutput{ExecutionID: executionID, Stream: stream, Reference: "output:" + executionID + ":" + stream, Complete: complete && spill.Complete, Redacted: redacted}
		if _, err := outputs.Attach(ctx, root.Identity.ProgramID, output, spill); err != nil {
			return errors.Join(err, spill.Close())
		}
		return nil
	}
	run.capture = invocation.ProgramInputCapture{State: memory, Outputs: outputs, Projector: r.Projector, ProgramID: root.Identity.ProgramID, Scope: scope, Screening: screening, MaximumNodes: 1024, MaximumDepth: 64}
	return nil
}

// bindProgramSurface composes the reached owners and freezes their operation
// surface before the program receipt begins. Its utility lease follows the run.
func (r *ProgramRuntime) bindProgramSurface(ctx context.Context, run *programRun, owners ProgramOwners, paths *projectpaths.ProgramPaths) (*ProgramDispatcher, commandprogram.OutputPublication, func(), error) {
	tc, memory := run.tc, run.memory
	external, err := run.externalDefinition()
	if err != nil {
		return nil, nil, nil, err
	}
	var releaseSurface func()
	complete := false
	defer func() {
		if !complete && releaseSurface != nil {
			releaseSurface()
		}
	}()
	jobs := r.Supervisor.Jobs(tc.Identity.SessionID, tc.Identity.ProjectID)
	builtins := jobBuiltins(jobs, run.commands)
	builtins["lsof"] = lsofBuiltin(run.commands["lsof"], tc.Identity.SessionID, jobs, r.Ports, tc.Execution.ProcessReview)
	builtins["sleep"] = sleepBuiltin()
	dispatcher := &ProgramDispatcher{run: run, builtins: builtins, natives: owners.Natives, binder: commandprogram.CatalogBinder{}, paths: paths, shared: &dispatchShared{}, redirections: owners.Redirections}
	builtins["timeout"] = timeoutBuiltin(run.commands["timeout"], dispatcher, r.clock())
	builtins["nohup"] = nohupBuiltin(run.commands["nohup"], dispatcher)
	var publication commandprogram.OutputPublication
	if owners.Publication != nil {
		publication = owners.Publication(dispatcher.beginOutput)
	}
	if r.Builtins != nil {
		for name, builtin := range r.Builtins(tc, dispatcher, dispatcher) {
			if _, taken := builtins[name]; !taken {
				builtins[name] = builtin
			}
		}
	}
	if owners.Builtins != nil {
		utilities, releaseUtilities, err := owners.Builtins(BuiltinPorts{ToolContext: tc, Memory: memory, ProgramID: run.root.Identity.ProgramID, Nested: dispatcher, Publication: publication, Admission: r.sessionResources(tc.Identity.SessionID)})
		releaseSurface = releaseUtilities
		if err != nil {
			return nil, nil, nil, err
		}
		for name, builtin := range utilities {
			if _, taken := builtins[name]; !taken {
				builtins[name] = builtin
			}
		}
	}
	active := []toolcontract.OperationDefinition{external}
	if write, ok := r.Registry.Operation(programPublicationTool); ok {
		active = append(active, offeredProgramDefinition(programPublicationTool, write.Operation))
	}
	seen := map[string]bool{external.ReadOnlyFacts().Identity: true, programPublicationTool: true}
	for _, operation := range owners.Natives {
		for _, tool := range append([]string{operation.Tool}, operation.Tools...) {
			if tool == "" || seen[tool] {
				continue
			}
			seen[tool] = true
			if definition, ok := r.Registry.Operation(tool); ok {
				active = append(active, offeredProgramDefinition(tool, definition.Operation))
			} else if spec, ok := run.catalogCommand(tool); ok {
				definition, err := catalogOperationDefinition(spec, run.catalog.Language.Limits)
				if err != nil {
					return nil, nil, nil, err
				}
				active = append(active, definition)
			}
		}
	}
	run.control.available = map[string]bool{}
	for name := range builtins {
		run.control.available[name] = true
	}
	for name := range pathCommands {
		run.control.available[name] = true
	}
	for name := range owners.Natives {
		run.control.available[name] = true
	}
	run.control.taken = seen
	definitions, err := run.controlDefinitions()
	if err != nil {
		return nil, nil, nil, err
	}
	for _, name := range []string{"cd", "realpath", "readlink", "native_path"} {
		spec, ok := run.catalogCommand(name)
		if !ok {
			return nil, nil, nil, fmt.Errorf("path command %s missing from catalog", name)
		}
		definition, err := catalogOperationDefinition(spec, run.catalog.Language.Limits)
		if err != nil {
			return nil, nil, nil, err
		}
		definitions[name] = definition
	}
	hostCommands := make([]string, 0, len(definitions))
	for name := range definitions {
		hostCommands = append(hostCommands, name)
	}
	slices.Sort(hostCommands)
	for _, name := range hostCommands {
		active = append(active, definitions[name])
	}
	surface, err := r.Executor.Metadata.FreezeProgramSurface(ctx, platform.ToolFilter{ProfileID: tc.ProfileID(), SurfaceID: tc.Turn.TurnSurfaceID}, active)
	if err != nil {
		return nil, nil, nil, err
	}

	run.surface = surface
	complete = true
	return dispatcher, publication, releaseSurface, nil
}

// parseProgram validates the entire unit; an excluded construct anywhere
// refuses before the first statement could run.
func parseProgram(ctx context.Context, memory *toolcontract.ProgramMemory, source string) (*commandsurface.ParsedProgram, *commandsurface.ProgramSnapshotLease, error) {
	parsed, err := commandsurface.ParseAdmitted(ctx, memory, []byte(source), commandsurface.ParseOptions{})
	if err != nil {
		if reject := toolcommand.ProgramSyntaxObservation("command", err); reject != nil {
			return nil, nil, reject
		}
		return nil, nil, err
	}
	snapshot, err := parsed.SnapshotWithMemory(ctx, memory)
	if err != nil {
		_ = parsed.Close()
		return nil, nil, err
	}
	return parsed, snapshot, nil
}

func (r *ProgramRuntime) beginProgram(ctx context.Context, run *programRun, outputs *invocation.ProgramOutputRetention, plan commandsurface.ProgramSnapshot, scope captureprojection.Scope, screening captureprojection.StreamOptions) error {
	sourceRef := "output:program-source:" + run.root.Identity.ProgramID
	var nodes []toolcontract.ProgramNode
	for _, location := range commandsurface.ExecutionLocations(plan.Statements) {
		nodes = append(nodes, toolcontract.ProgramNode{ID: string(location.ID), SourceUnitID: "submitted", Span: location.Span})
	}
	start := toolcontract.ProgramStart{
		Version:       toolcontract.ProgramVersion{ProgramID: run.root.Identity.ProgramID, ParentReceiptID: run.parentReceipt, RootExecutionID: run.root.Identity.ExecutionID, LanguageVersion: plan.Version, CatalogDigest: plan.CatalogDigest, SourceUnitID: "submitted", SourceDigest: plan.SourceDigest, SourceReference: sourceRef},
		PlanReference: sourceRef, SourceExact: true, SyntaxValidated: true,
		Sources: []toolcontract.ProgramSource{{UnitID: "submitted", Pin: toolcontract.SourcePin{SourceID: "submitted", Revision: plan.SourceDigest, Representation: toolcontract.UTF8SourceBytes, SHA256: plan.SourceDigest, EndByte: int64(len(run.source))}, DetailReference: sourceRef}},
		Nodes:   nodes,
	}
	spill, redacted, err := r.retainScreened(ctx, run.memory, run.root.Identity.ProgramID, []byte(run.source), scope, screening)
	if err != nil {
		return err
	}
	return outputs.BeginProgram(ctx, start, spill, redacted)
}

func (r *ProgramRuntime) retainScreened(ctx context.Context, memory *toolcontract.ProgramMemory, claim string, content []byte, scope captureprojection.Scope, screening captureprojection.StreamOptions) (*tooloutput.StreamSpill, bool, error) {
	var screened bytes.Buffer
	metadata, err := r.Projector.ScreenStream(ctx, scope, "capture.native", bytes.NewReader(content), &screened, screening)
	if err != nil {
		return nil, false, err
	}
	spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(io.NopCloser(bytes.NewReader(screened.Bytes()))), tooloutput.StreamSpillOptions{
		Store: r.Recorder.ProgramBlobStore(ctx, r.Blobs), ClaimID: claim, MaximumBytes: 1 << 20, PreviewBytes: 4 << 10, Admission: r.sessionResources(scope.SessionID), WorkingMemory: memory, RetainedState: r.sessionResources(scope.SessionID),
	})
	if err != nil {
		return nil, false, err
	}
	return spill, metadata.Redacted, nil
}

// settleProgram records the evaluator's aggregate; a supervisor stop or loss
// overrides it with the interruption RunProgram reported.
func (r *ProgramRuntime) settleProgram(ctx context.Context, run *programRun, result toolcontract.ProducerResult, evaluated commandprogram.ProgramOutcome, stopped bool) error {
	settlement := evaluated.Settlement("output:program-source:" + run.root.Identity.ProgramID)
	if stopped || result.Terminal == toolcontract.ProducerInterrupted {
		status := 130
		settlement.State, settlement.Status, settlement.StopCode = "interrupted", &status, toolrejection.ToolOwnerInterruptedCode
	}
	return r.Recorder.SettleProgram(context.WithoutCancel(ctx), run.root.Identity.ProgramID, settlement)
}

// evaluatedResult is the aggregate utility outcome of an evaluated program.
func evaluatedResult(outcome commandprogram.ProgramOutcome) toolcontract.ProducerResult {
	if outcome.Stop != nil {
		return outcome.Stop.Result
	}
	return toolcontract.ProducerResult{Status: outcome.Status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: outcome.Status == 0, EffectCertainty: toolcontract.EffectUnknown}
}

// feedback renders the first native owner rejection through the program
// feedback boundary, with the program's completed and uncertain effects.
func (r *ProgramRuntime) feedback(ctx context.Context, run *programRun, dispatcher *ProgramDispatcher, stderr io.Writer, evalErr error) (refusal *ProgramRefusal, release func(), err error) {
	defer func() {
		if release == nil {
			release = func() {}
		}
	}()
	rejection := dispatcher.firstRejection()

	if rejection == nil {
		var rejection *toolrejection.ToolReject
		var evaluationRefusal *commandprogram.EvaluationRefusal
		if errors.As(evalErr, &evaluationRefusal) {
			span := evaluationRefusal.Node.Span
			data := map[string]any{"tool": "command", "source_start_byte": span.StartByte, "source_end_byte": span.EndByte, "source_line": span.Line, "source_column": span.Column, "expected": evaluationRefusal.Expected, "expected_form": evaluationRefusal.Expected, "reason": evaluationRefusal.Expected}
			var expansion *commandprogram.ExpansionError
			if errors.As(evaluationRefusal.Cause, &expansion) && expansion.Limit != "" {
				data["limit_id"], data["maximum"], data["observed"] = expansion.Limit, expansion.Maximum, expansion.ObservedValue()
			}
			if evaluationRefusal.Command != "" {
				data["command"], data["word"], data["expected_form"] = evaluationRefusal.Command, evaluationRefusal.Option, evaluationRefusal.Form
			}
			rejection = toolrejection.RejectInvalidArguments(evaluationRefusal.Code, data)
			original := toolrejection.AsToolReject(tools.CommandResourceRejection(evaluationRefusal.Cause))
			if original != nil && original.Code == evaluationRefusal.Code {
				copy := *original
				copy.Data = data
				for key, value := range original.Data {
					copy.Data[key] = value
				}
				rejection = &copy
			}
			enrichProgramReject(rejection, evaluationRefusal.Command, span)
			if rejection.Code == "COMMAND_LIMIT_EXCEEDED" && dispatcher.claimStopLine() {
				writeLimitRefusal(stderr, "command", "", rejection)
			}
		} else if rejection = toolrejection.AsToolReject(evalErr); rejection != nil {
			enrichProgramReject(rejection, "", run.root.Span)
		} else {
			return nil, nil, evalErr
		}
		delivery, err := run.executor.Rejections.RenderProgramRefusal(ctx, run.frame, run.memory, run.capture, run.tc, errors.Join(rejection, evalErr), nil)
		if err != nil {
			return nil, nil, errors.Join(evalErr, err)
		}
		refusal = &ProgramRefusal{Code: delivery.Code, Details: delivery.Details, DeliveryFailure: delivery.Failure}
		if delivery.Feedback != nil {
			refusal.Feedback = delivery.Feedback.Error()
		}
		return refusal, delivery.Close, nil
	}

	reject := toolrejection.AsToolReject(rejection.cause)
	enrichProgramReject(reject, rejection.operation.Command(), rejection.operation.Invocation().Span)
	failedDelivery := func(cause error) (*ProgramRefusal, func(), error) {
		var capacity *toolcontract.ProgramMemoryLimitError
		if !errors.As(cause, &capacity) && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			return nil, nil, errors.Join(rejection.cause, cause)
		}
		delivery, err := run.executor.Rejections.RenderProgramRefusal(ctx, run.frame, run.memory, run.capture, run.tc, rejection.cause, cause)
		if err != nil {
			return nil, nil, errors.Join(rejection.cause, err)
		}
		return &ProgramRefusal{Code: delivery.Code, DeliveryFailure: delivery.Failure}, delivery.Close, nil
	}
	prepared, err := toolexecution.PrepareProgramFailure(ctx, run.frame, run.recorder, run.memory, run.capture, run.tc, rejection.operation, rejection.cause)
	if err != nil {
		return failedDelivery(err)
	}
	if prepared == nil {
		return nil, nil, rejection.cause
	}
	release = prepared.Close
	rendered := run.executor.Rejections.RenderProgramFailure(ctx, run.frame, run.tc, prepared)
	if rendered != nil && toolrejection.AsToolReject(rendered) == nil {
		prepared.Close()
		return failedDelivery(rendered)
	}
	if rendered == nil {
		rendered = toolrejection.RenderReject(prepared.Rejection, nil)
	}
	return &ProgramRefusal{Code: reject.Code, Feedback: rendered.Error(), Details: prepared.Rejection.Data}, release, nil
}

func (r *ProgramRuntime) sessionOutputs(ctx context.Context, sessionID string) (*invocation.ProgramOutputRetention, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if outputs, ok := r.outputs[sessionID]; ok {
		return outputs, nil
	}
	outputs, err := invocation.NewProgramOutputRetention(ctx, r.Recorder, r.Blobs, sessionID, r.sessionResourcesLocked(sessionID))
	if err != nil {
		return nil, err
	}
	if err := outputs.Restore(ctx); err != nil {
		return nil, err
	}
	if r.outputs == nil {
		r.outputs = map[string]*invocation.ProgramOutputRetention{}
	}
	r.outputs[sessionID] = outputs
	return outputs, nil
}

// boundedBuffer keeps the first limit bytes and records truncation; the
// producer is never blocked or failed by a full display buffer.
type boundedBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - len(b.data)
	if room < len(p) {
		b.truncated = true
		if room > 0 {
			b.data = append(b.data, p[:room]...)
		}
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

var compiledCommandIndex = sync.OnceValues(func() (map[string]toolcontract.CommandSpecification, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	return commandIndex(catalog), nil
})

var compiledExternalDefinition = sync.OnceValues(func() (toolcontract.OperationDefinition, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return toolcontract.OperationDefinition{}, err
	}
	commands, err := compiledCommandIndex()
	if err != nil {
		return toolcontract.OperationDefinition{}, err
	}
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		return toolcontract.OperationDefinition{}, fmt.Errorf("command contract missing")
	}
	return toolcontract.NewOperationDefinition(toolcontract.OperationFacts{
		Identity: "command", Version: 1, Contract: contract, Arguments: json.RawMessage(`{"type":"object"}`),
		Commands: []toolcontract.CommandSpecification{commands["external"]}, Resources: catalog.Language.Limits,
		ProgressGate: toolcontract.ProgressExistingAdmission, RejectionContract: "command",
		Streams: toolcontract.StreamContract{Input: "exact process stdin bytes", Output: "exact process stdout bytes", Error: "process stderr; typed host refusals", Statuses: map[int]string{0: "process exit status"}},
	})
})

// externalDefinition is the command tool's operation for confined launches:
// the existing command review, detection and policy admit each launch.
func (r *programRun) externalDefinition() (toolcontract.OperationDefinition, error) {
	return compiledExternalDefinition()
}

// launchCapabilityFields are the call's declarations that every confined launch
// it reaches is reviewed with: confinement capabilities, SOCKS injection and
// managed-secret use.
var launchCapabilityFields = []string{"capability_request", "socks_proxy", "secret_use"}

func launchEnvelope(args map[string]any) map[string]any {
	envelope := map[string]any{}
	for _, field := range launchCapabilityFields {
		if value, ok := args[field]; ok {
			envelope[field] = value
		}
	}
	return envelope
}

// launchArguments are the arguments the existing command review, detection
// and policy evaluate for one launch.
func (r *programRun) launchArguments(line, cwd string) map[string]any {
	arguments := map[string]any{"command": line, "cwd": cwd}
	for field, value := range r.envelope {
		arguments[field] = value
	}
	return arguments
}

// nativeArguments attaches any call-level capability declarations that the
// bound native operation contract accepts.
func (r *programRun) nativeArguments(base map[string]any, contract toolcontract.Contract, loopbackPorts []uint16) map[string]any {
	arguments := make(map[string]any, len(base)+len(r.envelope))
	for k, v := range base {
		arguments[k] = v
	}
	if raw, ok := r.envelope["capability_request"].(map[string]any); ok {
		supported := map[string]any{}
		for _, field := range contract.CapabilityRequestFields() {
			if value, exists := raw[field]; exists {
				supported[field] = value
			}
		}
		if len(supported) > 0 {
			arguments["capability_request"] = supported
		}
	}
	foreignPorts := make([]uint16, 0, len(loopbackPorts))
	for _, port := range loopbackPorts {
		if !slices.Contains(r.tc.Local.OwnedLoopbackPorts, port) {
			foreignPorts = append(foreignPorts, port)
		}
	}
	if len(foreignPorts) > 0 && contract.Supports(toolcontract.CapabilityLoopbackConnect) {
		request, _ := arguments["capability_request"].(map[string]any)
		if request == nil {
			request = map[string]any{}
			arguments["capability_request"] = request
		}
		if _, explicit := request["loopback_connect"]; !explicit {
			ports := make([]any, len(foreignPorts))
			for i, port := range foreignPorts {
				ports[i] = int(port)
			}
			request["loopback_connect"] = map[string]any{"ports": ports}
		}
	}
	if secretUse, ok := r.envelope["secret_use"]; ok && contract.SecretReferenceSurface != "" {
		arguments["secret_use"] = secretUse
	}
	return arguments
}

func (r *programRun) sourceText(unitID string) string {
	if unitID == "" || unitID == "submitted" {
		return r.source
	}
	r.sourcesMu.RLock()
	defer r.sourcesMu.RUnlock()
	if s, ok := r.sources[unitID]; ok {
		return s
	}
	return r.source
}

func (r *programRun) setSourceText(unitID, text string) {
	if unitID == "" {
		return
	}
	r.sourcesMu.Lock()
	if r.sources == nil {
		r.sources = map[string]string{}
	}
	r.sources[unitID] = text
	r.sourcesMu.Unlock()
}

// childOutputBytes bounds each child stream the output store retains.
const childOutputBytes = 64 << 10

// childOutputs wraps a child's streams so source mappings on top-level stdout
// carry their stream offset and only a failed child's stderr is retained per
// execution; pipe stages produce no retention rows.
func (r *programRun) childOutputs(request commandprogram.OperationRequest, streams commandprogram.Streams) (commandprogram.Streams, func(context.Context, toolcontract.ProducerResult, error)) {
	teed := streams
	outputView, errorView := newChildStreamView(streams.Output), newChildStreamView(streams.Error)
	if outputView != nil {
		teed.Output = outputView
	}
	errorOutput := nonNilWriter(streams.Error)
	if errorView != nil {
		errorOutput = errorView
	}
	closeViews := func() {
		if outputView != nil {
			outputView.Close()
		}
		if errorView != nil {
			errorView.Close()
		}
	}
	if r.retainOutput == nil {
		teed.Error = errorOutput
		return teed, func(context.Context, toolcontract.ProducerResult, error) { closeViews() }
	}
	stderr := &boundedBuffer{limit: childOutputBytes}
	if errorView != nil {
		errorView.copy = stderr
		teed.Error = errorView
	} else {
		teed.Error = io.MultiWriter(errorOutput, stderr)
	}
	execution := request.Invocation.Identity.ExecutionID
	return teed, func(ctx context.Context, result toolcontract.ProducerResult, err error) {
		defer closeViews()
		failed := err != nil || result.Status != 0 || (result.Terminal != "" && result.Terminal != toolcontract.ProducerCompleted)
		if !failed {
			return
		}
		if data := stderr.Bytes(); len(data) > 0 {
			_ = r.retainOutput(context.WithoutCancel(ctx), execution, "stderr", data, !stderr.truncated)
		}
	}
}
