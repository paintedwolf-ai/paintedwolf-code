package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
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
	// Builtins supplies host utilities for one invocation; nested runs their command operands.
	Builtins func(tools.ToolContext, commandprogram.NestedCommand, NativeBindings) map[string]commandprogram.OperationDispatcher
	// Owners composes one program's host owner set from its root directory frame.
	Owners func(tools.ToolContext, *projectpaths.ProgramPaths) (ProgramOwners, error)
	// Listeners observes session-owned listeners; nil uses process lineage.
	Listeners SessionListeners
	// ExternalHidden refuses the explicit `external --` selector; native gap
	// launches still run and are recorded.
	ExternalHidden bool

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

// SessionOwners reads the session whose owner a program acts for.
type SessionOwners interface {
	Get(context.Context, string) (*api.Session, error)
}

// programRun is one invocation's frozen frame, surface and ledger bindings.
type programRun struct {
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
	baseEnv       []string

	// envelope carries the call's launch capability requests to each launch.
	envelope map[string]any

	externalOnce sync.Once
	external     toolcontract.OperationDefinition
	externalErr  error
}

// ProgramOutcome is the command tool's result for a settled program.
type ProgramOutcome struct {
	ProgramID        string `json:"program_id"`
	State            string `json:"state"`
	Status           int    `json:"status"`
	StopCode         string `json:"stop_code,omitempty"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	Truncated        bool   `json:"truncated,omitempty"`
	ExecutedChildren int    `json:"executed_children"`
	CompletedEffects int    `json:"completed_effects"`
	UncertainEffects int    `json:"uncertain_effects"`
	NativeGaps       int    `json:"native_gaps,omitempty"`
	ReplayAvailable  bool   `json:"replay_available"`
	// Refusal is the host decision that stopped the program; the output and
	// status of every child that ran before it stay in this outcome.
	Refusal *ProgramRefusal `json:"refusal,omitempty"`
}

// ProgramRefusal is a typed host refusal at the program's stop point with its
// rendered feedback.
type ProgramRefusal struct {
	Code     string `json:"code"`
	Feedback string `json:"feedback"`
}

const programOutputBytes = 64 << 10

// Run parses and validates the whole submitted program before any effect,
// freezes frame and surface, records the program, evaluates it under
// supervision, settles it and returns the bounded owner projection.
func (r *ProgramRuntime) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	source, _ := args["command"].(string)
	if source == "" {
		return "", toolrejection.RejectInvalidArguments("COMMAND_ARGV_REQUIRED", map[string]any{"field": "command"})
	}
	if r != nil && r.disabled.Load() {
		return "", toolrejection.RejectInvalidArguments("COMMAND_PROGRAMS_DISABLED", map[string]any{"tool": "command"})
	}
	if timeout := toolkit.ClampIntArg(args, "timeout_ms", 0, 0, 7200000); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()
	}
	if r == nil || r.Executor == nil || r.Recorder == nil || r.Evaluate == nil || r.Supervisor == nil || r.Sessions == nil || r.Projector == nil {
		return "", fmt.Errorf("command program runtime is not configured")
	}
	catalog, err := toolcontract.LanguageV1()
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
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()

	cwd, _ := args["cwd"].(string)
	paths, err := projectpaths.NewProgramPaths(ctx, r.Boundary, tc, cwd)
	if err != nil {
		return "", err
	}
	session, err := r.Sessions.Get(ctx, tc.Identity.SessionID)
	if err != nil {
		return "", err
	}
	if session == nil || session.OwnerPersonID == "" {
		return "", fmt.Errorf("command program requires the session owner")
	}
	branch, err := tc.SourceBranch(tc.Source.ActiveRootID)
	if err != nil {
		return "", err
	}
	root := toolcontract.OperationInvocation{
		Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: uuid.NewString(), NodeID: "program", ExecutionID: uuid.NewString()},
		ActorID: session.OwnerPersonID, SessionID: tc.Identity.SessionID,
		Scope:        toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: tc.Identity.ProjectID, RootID: tc.Source.ActiveRootID, BranchID: branch.String(), Directory: cwd},
		SourceUnitID: "submitted", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: snapshot.Plan.CatalogDigest,
	}
	frame, err := toolexecution.FreezeProgramFrame(ctx, root, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: args}, memory)
	if err != nil {
		return "", err
	}
	releasePlan := release
	release = func() { frame.Close(); releasePlan() }
	// A validation run's source identity is captured before anything runs.
	var verification func() commandSourceIdentity
	if tc.Execution.VerificationCheck {
		_, reader := commandSourceReaders(ctx, tc, paths.Cwd().Abs)
		identity := reader()
		verification = func() commandSourceIdentity { return identity }
	}
	var owners ProgramOwners
	if r.Owners != nil {
		if owners, err = r.Owners(tc, paths); err != nil {
			return "", err
		}
	}
	run := &programRun{catalog: catalog, commands: commandIndex(catalog), baseEnv: programEnvironment(tc), envelope: launchEnvelope(args)}
	external, err := run.externalDefinition()
	if err != nil {
		return "", err
	}
	// Many command names share one operation owner; each definition enters
	// the surface once, keyed by its operation identity.
	active := []toolcontract.OperationDefinition{external}
	seen := map[string]bool{}
	for _, operation := range owners.Natives {
		for _, tool := range append([]string{operation.Tool}, operation.Tools...) {
			if seen[tool] {
				continue
			}
			seen[tool] = true
			if definition, ok := r.Registry.Definition(tool); ok {
				active = append(active, programOperationDefinition(definition.Operation))
			}
		}
	}
	surface, err := r.Executor.Metadata.FreezeProgramSurface(ctx, platform.ToolFilter{ProfileID: tc.ProfileID(), SurfaceID: tc.Turn.TurnSurfaceID}, active)
	if err != nil {
		return "", err
	}
	outputs, err := r.sessionOutputs(ctx, tc.Identity.SessionID)
	if err != nil {
		return "", err
	}
	scope := captureprojection.Scope{ProjectID: tc.Identity.ProjectID, RootSessionID: tc.ChatSessionID(), SessionID: tc.Identity.SessionID}
	screening := captureprojection.StreamOptions{WorkingMemory: memory, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}
	run.executor, run.registry, run.recorder, run.supervisor, run.progress = r.Executor, r.Registry, r.Recorder, r.Supervisor, r.Progress
	run.externalHidden = r.ExternalHidden
	run.retainOutput = func(ctx context.Context, executionID, stream string, data []byte, complete bool) error {
		spill, redacted, err := r.retainScreened(ctx, memory, executionID+":"+stream, data, scope, screening)
		if err != nil {
			return err
		}
		output := invocation.RetainedOutput{ExecutionID: executionID, Stream: stream, Reference: "output:" + executionID + ":" + stream, Complete: complete && spill.Complete, Redacted: redacted}
		if err := outputs.Attach(ctx, root.Identity.ProgramID, output, spill); err != nil {
			return errors.Join(err, spill.Close())
		}
		return nil
	}
	run.frame, run.surface, run.memory, run.tc, run.root, run.parentReceipt, run.source = frame, surface, memory, tc, root, tc.Invocation.ID, source
	run.capture = invocation.ProgramInputCapture{State: memory, Outputs: outputs, Projector: r.Projector, ProgramID: root.Identity.ProgramID, Scope: scope, Screening: screening, MaximumNodes: 1024, MaximumDepth: 64}
	jobs := r.Supervisor.Jobs(tc.Identity.SessionID, tc.Identity.ProjectID)
	ctx, err = jobs.BeginProgram(ctx)
	if err != nil {
		return "", err
	}
	if err := r.beginProgram(ctx, run, outputs, snapshot.Plan, scope, screening); err != nil {
		return "", err
	}
	builtins := jobBuiltins(jobs, run.commands)
	listeners := r.Listeners
	if listeners == nil {
		listeners = lineage.SessionListeners
	}
	builtins["lsof"] = lsofBuiltin(run.commands["lsof"], tc.Identity.SessionID, listeners)
	builtins["sleep"] = sleepBuiltin()
	dispatcher := &ProgramDispatcher{run: run, builtins: builtins, natives: owners.Natives, binder: commandprogram.CatalogBinder{}, paths: paths, shared: &dispatchShared{}, admitLaunch: owners.AdmitLaunch, redirections: owners.Redirections}
	if r.Builtins != nil {
		for name, builtin := range r.Builtins(tc, dispatcher, dispatcher) {
			if _, taken := builtins[name]; !taken {
				builtins[name] = builtin
			}
		}
	}
	if owners.Builtins != nil {
		utilities, releaseUtilities, err := owners.Builtins(BuiltinPorts{ToolContext: tc, Memory: memory, ProgramID: root.Identity.ProgramID, Nested: dispatcher, Publication: owners.Publication, Admission: r.sessionResources(tc.Identity.SessionID)})
		if err != nil {
			return "", err
		}
		releaseBefore := release
		release = func() { releaseUtilities(); releaseBefore() }
		for name, builtin := range utilities {
			if _, taken := builtins[name]; !taken {
				builtins[name] = builtin
			}
		}
	}
	stdout, stderr := &boundedBuffer{limit: programOutputBytes}, &boundedBuffer{limit: programOutputBytes}
	sources := owners.Sources
	if sources == nil {
		if sc, ok := owners.Input.(commandprogram.SourceCapture); ok {
			sources = sc
		}
	}
	ports := commandprogram.EvaluationPorts{
		Invocation: root, Dispatch: dispatcher, Binder: commandprogram.CatalogBinder{}, Files: projectpaths.GlobFiles{Paths: paths},
		Publication: owners.Publication, Input: owners.Input, Sources: sources,
		Executions: dispatcher, Supervision: r.Supervisor, Predicates: commandprogram.PredicateRuntime{Patterns: commandprogram.NativePredicatePatterns{Memory: memory}},
		Memory:      memory,
		Environment: run.baseEnv,
		Streams:     commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr},
	}
	var evaluated commandprogram.ProgramOutcome
	result, evalErr := r.Supervisor.RunProgram(ctx, root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
		var err error
		evaluated, err = r.Evaluate(ctx, snapshot.Plan, ports)
		return evaluatedResult(evaluated), err
	})
	settled := make(chan error, 1)
	handedOff = true
	lifetime := &programLifetime{release: release}
	defer lifetime.callerDone()
	r.Supervisor.AfterJobs(root.Identity.ProgramID, func(stopped bool) {
		settled <- r.settleProgram(ctx, run, result, evaluated, stopped)
		lifetime.settled()
	})
	var settleErr error
	settledNow := false
	select {
	case settleErr = <-settled:
		settledNow = true
	default:
		// & jobs still run under supervision; the program settles when they end.
	}
	feedback := r.feedback(ctx, run, dispatcher, result.StopCode, evalErr)
	refusal := toolrejection.AsToolReject(feedback)
	if (feedback != nil && refusal == nil) || settleErr != nil {
		return "", errors.Join(feedback, settleErr)
	}
	outcome, err := r.outcome(ctx, run, result, stdout, stderr, scope, screening)
	if err != nil {
		return "", errors.Join(feedback, err)
	}
	if refusal != nil {
		outcome.Refusal = &ProgramRefusal{Code: refusal.Code, Feedback: feedback.Error()}
	}
	if settledNow {
		stampProgramRun(tc, source, paths.Cwd(), outcome, verification)
	}
	out, err := surveyjson.Marshal(outcome)
	if err != nil {
		return "", fmt.Errorf("command program encode: %w", err)
	}
	return string(out), nil
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
	for _, location := range programNodes(plan.Statements) {
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
		Store: r.Recorder.ProgramBlobStore(r.Blobs), ClaimID: claim, MaximumBytes: 1 << 20, PreviewBytes: 4 << 10, Admission: r.sessionResources(scope.SessionID), WorkingMemory: memory, RetainedState: memory,
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
func (r *ProgramRuntime) feedback(ctx context.Context, run *programRun, dispatcher *ProgramDispatcher, stopCode string, evalErr error) error {
	rejection := dispatcher.firstRejection()
	if rejection == nil {
		var refusal *commandprogram.EvaluationRefusal
		if errors.As(evalErr, &refusal) {
			span := refusal.Node.Span
			data := map[string]any{"tool": "command", "source_start_byte": span.StartByte, "source_end_byte": span.EndByte, "source_line": span.Line, "source_column": span.Column, "expected": refusal.Expected, "expected_form": refusal.Expected, "reason": refusal.Expected}
			if refusal.Command != "" {
				data["command"], data["word"], data["expected_form"] = refusal.Command, refusal.Option, refusal.Form
			}
			reject := toolrejection.RejectInvalidArguments(refusal.Code, data)
			enrichProgramReject(reject, refusal.Command, span)
			return ensureProgramRefusalText(toolrejection.RenderReject(reject, nil), reject)
		}
		if reject := toolrejection.AsToolReject(evalErr); reject != nil {
			enrichProgramReject(reject, "", run.root.Span)
			return ensureProgramRefusalText(toolrejection.RenderReject(reject, nil), reject)
		}
		if evalErr != nil && !errors.Is(evalErr, context.Canceled) && !errors.Is(evalErr, context.DeadlineExceeded) {
			code := stopCode
			if code == "" {
				code = toolrejection.ToolOwnerFailedCode
			}
			reject := &toolrejection.ToolReject{Code: code, FailureClass: "owner_error", Data: map[string]any{"tool": "command", "reason": evalErr.Error()}}
			enrichProgramReject(reject, "command", run.root.Span)
			return errors.Join(evalErr, ensureProgramRefusalText(toolrejection.RenderReject(reject, nil), reject))
		}
		// A host stop without a typed rejection is a host failure, never success.
		return evalErr
	}
	reject := toolrejection.AsToolReject(rejection.cause)
	enrichProgramReject(reject, rejection.operation.Command(), rejection.operation.Invocation().Span)
	prepared, err := toolexecution.PrepareProgramFailure(ctx, run.recorder, run.memory, run.capture, run.tc, rejection.operation, rejection.cause)
	if err != nil || prepared == nil {
		return errors.Join(ensureProgramRefusalText(toolrejection.RenderReject(reject, nil), reject), err)
	}
	defer prepared.Close()
	rendered := run.executor.Rejections.RenderProgramFailure(ctx, run.frame, run.tc, prepared)
	if rendered != nil && toolrejection.AsToolReject(rendered) == nil {
		return errors.Join(ensureProgramRefusalText(toolrejection.RenderReject(reject, nil), reject), rendered)
	}
	return ensureProgramRefusalText(rendered, reject)
}

func (r *ProgramRuntime) outcome(ctx context.Context, run *programRun, result toolcontract.ProducerResult, stdout, stderr *boundedBuffer, scope captureprojection.Scope, screening captureprojection.StreamOptions) (ProgramOutcome, error) {
	projection, err := r.Recorder.ProjectProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, 4)
	if err != nil {
		return ProgramOutcome{}, err
	}
	if run.tc.Effects.Out != nil {
		receipt := projection.Program
		run.tc.Effects.Out.CommandProgram = &receipt
	}
	out := ProgramOutcome{ProgramID: run.root.Identity.ProgramID, State: projection.Program.State, Status: result.Status, StopCode: result.StopCode, Truncated: stdout.truncated || stderr.truncated,
		ExecutedChildren: projection.ExecutedChildren, CompletedEffects: projection.CompletedEffects, UncertainEffects: projection.UncertainEffects, NativeGaps: projection.NativeGaps}
	for _, stream := range []struct {
		buffer *boundedBuffer
		target *string
	}{{stdout, &out.Stdout}, {stderr, &out.Stderr}} {
		var screened bytes.Buffer
		if _, err := r.Projector.ScreenStream(ctx, scope, "capture.native", bytes.NewReader(stream.buffer.Bytes()), &screened, screening); err != nil {
			return ProgramOutcome{}, err
		}
		*stream.target = screened.String()
	}
	return out, nil
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

// programNodes lists every static location an execution can begin at:
// statements (nested and inside substitutions), substitutions and redirections.
func programNodes(statements []commandsurface.Statement) []commandsurface.SyntaxLocation {
	var out []commandsurface.SyntaxLocation
	var statement func(commandsurface.Statement)
	var words func([]commandsurface.DeferredWord)
	var parts func([]commandsurface.WordNode)
	list := func(items []commandsurface.Statement) {
		for _, item := range items {
			statement(item)
		}
	}
	parts = func(nodes []commandsurface.WordNode) {
		for _, node := range nodes {
			switch node := node.(type) {
			case commandsurface.DoubleQuotedPart:
				parts(node.Parts)
			case commandsurface.SubstitutionPart:
				out = append(out, node.SyntaxLocation)
				list(node.Statements)
			case commandsurface.ParameterPart:
				words(node.Words())
			case commandsurface.ArithmeticPart:
				words(node.Words())
			}
		}
	}
	words = func(items []commandsurface.DeferredWord) {
		for _, item := range items {
			parts(item.Parts)
		}
	}
	statement = func(s commandsurface.Statement) {
		out = append(out, s.SyntaxLocation)
		for _, redirect := range s.Redirects {
			out = append(out, redirect.SyntaxLocation)
			words([]commandsurface.DeferredWord{redirect.Target})
		}
		switch node := s.Command.(type) {
		case commandsurface.SimpleCommand:
			words(node.Words)
			for _, assignment := range node.Assignments {
				words([]commandsurface.DeferredWord{assignment.Value})
			}
		case commandsurface.BinaryCommand:
			statement(node.Left)
			statement(node.Right)
		case commandsurface.StatementGroup:
			list(node.Statements)
		case commandsurface.Conditional:
			for branch := &node; branch != nil; branch = branch.Else {
				list(branch.Condition)
				list(branch.Then)
			}
		case commandsurface.WhileLoop:
			list(node.Condition)
			list(node.Body)
		case commandsurface.ForLoop:
			words(node.Items)
			list(node.Body)
		case commandsurface.CaseStatement:
			words([]commandsurface.DeferredWord{node.Word})
			for _, item := range node.Items {
				words(item.Patterns)
				list(item.Body)
			}
		case commandsurface.FunctionDeclaration:
			statement(node.Body)
		}
	}
	list(statements)
	seen := map[commandsurface.NodeID]bool{}
	unique := out[:0]
	for _, location := range out {
		if !seen[location.ID] {
			seen[location.ID] = true
			unique = append(unique, location)
		}
	}
	return unique
}

// externalDefinition is the command tool's operation for confined launches:
// the existing command review, detection and policy admit each launch.
func (r *programRun) externalDefinition() (toolcontract.OperationDefinition, error) {
	r.externalOnce.Do(func() {
		contract, ok := toolcontract.Lookup("command")
		if !ok {
			r.externalErr = fmt.Errorf("command contract missing")
			return
		}
		r.external, r.externalErr = toolcontract.NewOperationDefinition(toolcontract.OperationFacts{
			Identity: "command", Version: 1, Contract: contract, Arguments: json.RawMessage(`{"type":"object"}`),
			Commands: []toolcontract.CommandSpecification{r.commands["external"]}, Resources: r.catalog.Language.Limits,
			ProgressGate: toolcontract.ProgressExistingAdmission, RejectionContract: "command",
			Streams: toolcontract.StreamContract{Input: "exact process stdin bytes", Output: "exact process stdout bytes", Error: "process stderr; typed host refusals", Statuses: map[int]string{0: "process exit status"}},
		})
	})
	return r.external, r.externalErr
}

// programLifetime releases a program's plan and frame once both the tool call
// has finished with them and the program (with its & jobs) has settled.
type programLifetime struct {
	mu                     sync.Mutex
	callerFinished, isDone bool
	release                func()
}

func (l *programLifetime) callerDone() {
	l.mu.Lock()
	l.callerFinished = true
	done := l.isDone
	l.mu.Unlock()
	if done {
		l.release()
	}
}

func (l *programLifetime) settled() {
	l.mu.Lock()
	l.isDone = true
	finished := l.callerFinished
	l.mu.Unlock()
	if finished {
		l.release()
	}
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
func (r *programRun) nativeArguments(base map[string]any, contract toolcontract.Contract) map[string]any {
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
	if secretUse, ok := r.envelope["secret_use"]; ok && contract.SecretReferenceSurface != "" {
		arguments["secret_use"] = secretUse
	}
	return arguments
}

// childOutputBytes bounds each child stream the output store retains.
const childOutputBytes = 64 << 10

// childOutputs tees a child's stdout and stderr into bounded buffers and
// returns the streams to use and a function that retains what was written.
func (r *programRun) childOutputs(request commandprogram.OperationRequest, streams commandprogram.Streams) (commandprogram.Streams, func(context.Context)) {
	if r.retainOutput == nil {
		return streams, func(context.Context) {}
	}
	stdout, stderr := &boundedBuffer{limit: childOutputBytes}, &boundedBuffer{limit: childOutputBytes}
	teed := streams
	teed.Output = io.MultiWriter(nonNilWriter(streams.Output), stdout)
	teed.Error = io.MultiWriter(nonNilWriter(streams.Error), stderr)
	execution := request.Invocation.Identity.ExecutionID
	return teed, func(ctx context.Context) {
		for _, stream := range []struct {
			name   string
			buffer *boundedBuffer
		}{{"stdout", stdout}, {"stderr", stderr}} {
			if data := stream.buffer.Bytes(); len(data) > 0 {
				// Retention is evidence for previews; a failure here never
				// changes the child's settled outcome.
				_ = r.retainOutput(context.WithoutCancel(ctx), execution, stream.name, data, !stream.buffer.truncated)
			}
		}
	}
}

// stampProgramRun states the settled program's run for verification evidence:
// passed on status 0, failed otherwise, unverifiable when the host stopped it.
// A program whose jobs still run states nothing.
func stampProgramRun(tc tools.ToolContext, source string, cwd projectpaths.Resolved, outcome ProgramOutcome, verification func() commandSourceIdentity) {
	if tc.Effects.Out == nil {
		return
	}
	verdict := api.SourceVerdictPassed
	switch {
	case outcome.Refusal != nil || outcome.State == "interrupted":
		verdict = api.SourceVerdictUnverifiable
	case outcome.Status != 0:
		verdict = api.SourceVerdictFailed
	}
	run := &tools.SourceRunCapture{CheckID: tc.Identity.ToolCallID, IsCheck: tc.Execution.VerificationCheck, Command: source, ExitCode: outcome.Status, Verdict: verdict, Cwd: cwd.DisplayPath}
	if tc.Execution.VerificationCheck && verification != nil {
		identity := verification()
		run.SourceRevision, run.SourceRootDigest = identity.Revision, identity.Root
	}
	tc.Effects.Out.SourceRun = run
}
