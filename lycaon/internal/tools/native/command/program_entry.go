package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"sync"
	"sync/atomic"

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
	Builtins func(tools.ToolContext, commandprogram.NestedCommand) map[string]commandprogram.OperationDispatcher
	// Owners composes one program's host owner set from its root directory frame.
	Owners func(tools.ToolContext, *projectpaths.ProgramPaths) (ProgramOwners, error)
	// Listeners observes session-owned listeners; nil uses process lineage.
	Listeners SessionListeners

	mu            sync.Mutex
	outputs       map[string]*invocation.ProgramOutputRetention
	resources     *sessionOutputResources
	resourcesOnce sync.Once
	disabled      atomic.Bool
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
	ReplayAvailable  bool   `json:"replay_available"`
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
	session, err := r.Sessions.Get(ctx, tc.Identity.SessionID)
	if err != nil {
		return "", err
	}
	if session == nil || session.OwnerPersonID == "" {
		return "", fmt.Errorf("command program requires the session owner")
	}
	parsed, snapshot, err := parseProgram(ctx, memory, source)
	if err != nil {
		return "", err
	}
	defer parsed.Close()
	defer snapshot.Close()

	root := toolcontract.OperationInvocation{
		Kind:         toolcontract.ProgramChildInvocation,
		Identity:     toolcontract.ExecutionIdentity{ProgramID: uuid.NewString(), NodeID: "program", ExecutionID: uuid.NewString()},
		ActorID:      session.OwnerPersonID,
		SessionID:    tc.Identity.SessionID,
		Scope:        toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: tc.Identity.ProjectID, RootID: tc.Source.ActiveRootID},
		SourceUnitID: "submitted", Span: toolcontract.SourceSpan{Line: 1, Column: 1},
		CatalogDigest: snapshot.Plan.CatalogDigest,
	}
	if cwd, _ := args["cwd"].(string); cwd != "" {
		root.Scope.Directory = cwd
	}
	frame, err := toolexecution.FreezeProgramFrame(ctx, root, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: args}, memory)
	if err != nil {
		return "", err
	}
	defer frame.Close()
	paths, err := projectpaths.NewProgramPaths(ctx, r.Boundary, tc, root.Scope.Directory)
	if err != nil {
		return "", err
	}
	var owners ProgramOwners
	if r.Owners != nil {
		if owners, err = r.Owners(tc, paths); err != nil {
			return "", err
		}
	}
	var active []toolcontract.OperationDefinition
	seen := map[string]bool{}
	for _, operation := range owners.Natives {
		for _, tool := range append([]string{operation.Tool}, operation.Tools...) {
			if seen[tool] {
				continue
			}
			seen[tool] = true
			if definition, ok := r.Registry.Definition(tool); ok {
				active = append(active, definition.Operation)
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
	run := &programRun{
		executor: r.Executor, registry: r.Registry, recorder: r.Recorder, supervisor: r.Supervisor, progress: r.Progress,
		frame: frame, surface: surface, memory: memory, tc: tc, root: root, parentReceipt: tc.Invocation.ID, source: source,
		catalog: catalog, commands: commandIndex(catalog),
		capture: invocation.ProgramInputCapture{State: memory, Outputs: outputs, Projector: r.Projector, ProgramID: root.Identity.ProgramID, Scope: scope, Screening: screening, MaximumNodes: 1024, MaximumDepth: 64},
	}
	if err := r.beginProgram(ctx, run, outputs, snapshot.Plan, scope, screening); err != nil {
		return "", err
	}
	jobs := r.Supervisor.Jobs(tc.Identity.SessionID, tc.Identity.ProjectID)
	builtins := jobBuiltins(jobs, run.commands)
	listeners := r.Listeners
	if listeners == nil {
		listeners = lineage.SessionListeners
	}
	builtins["lsof"] = lsofBuiltin(run.commands["lsof"], tc.Identity.SessionID, listeners)
	dispatcher := &ProgramDispatcher{run: run, builtins: builtins, natives: owners.Natives, binder: commandprogram.CatalogBinder{}, paths: paths, shared: &dispatchShared{}, admitLaunch: owners.AdmitLaunch}
	if r.Builtins != nil {
		for name, builtin := range r.Builtins(tc, dispatcher) {
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
		Memory:  memory,
		Streams: commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr},
	}
	var evaluated commandprogram.ProgramOutcome
	result, evalErr := r.Supervisor.RunProgram(ctx, root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
		var err error
		evaluated, err = r.Evaluate(ctx, snapshot.Plan, ports)
		return evaluatedResult(evaluated), err
	})
	settled := make(chan error, 1)
	r.Supervisor.AfterJobs(root.Identity.ProgramID, func(stopped bool) { settled <- r.settleProgram(ctx, run, result, evaluated, stopped) })
	var settleErr error
	select {
	case settleErr = <-settled:
	default:
		// & jobs still run under supervision; the program settles when they end.
	}
	if feedback := r.feedback(ctx, run, dispatcher, evalErr); feedback != nil {
		return "", errors.Join(feedback, settleErr)
	}
	if settleErr != nil {
		return "", settleErr
	}
	outcome, err := r.outcome(ctx, run, result, stdout, stderr, scope, screening)
	if err != nil {
		return "", err
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
		Store: r.Recorder.ProgramBlobStore(r.Blobs), ClaimID: claim, MaximumBytes: 1 << 20, Admission: r.sessionResources(), WorkingMemory: memory, RetainedState: memory,
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
	if stopped {
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
func (r *ProgramRuntime) feedback(ctx context.Context, run *programRun, dispatcher *ProgramDispatcher, evalErr error) error {
	rejection := dispatcher.firstRejection()
	if rejection == nil {
		var refusal *commandprogram.EvaluationRefusal
		if errors.As(evalErr, &refusal) {
			span := refusal.Node.Span
			return toolrejection.RejectInvalidArguments(refusal.Code, map[string]any{"tool": "command", "source_start_byte": span.StartByte, "source_end_byte": span.EndByte, "source_line": span.Line, "source_column": span.Column, "expected_form": refusal.Expected, "reason": refusal.Expected})
		}
		if reject := toolrejection.AsToolReject(evalErr); reject != nil {
			return reject
		}
		// A host stop without a typed rejection is a host failure, never success.
		return evalErr
	}
	prepared, err := toolexecution.PrepareProgramFailure(ctx, run.recorder, run.memory, run.capture, run.tc, rejection.operation, rejection.cause)
	if err != nil || prepared == nil {
		return errors.Join(rejection.cause, err)
	}
	defer prepared.Close()
	return run.executor.Rejections.RenderProgramFailure(ctx, run.frame, run.tc, prepared)
}

func (r *ProgramRuntime) outcome(ctx context.Context, run *programRun, result toolcontract.ProducerResult, stdout, stderr *boundedBuffer, scope captureprojection.Scope, screening captureprojection.StreamOptions) (ProgramOutcome, error) {
	projection, err := r.Recorder.ProjectProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, 4)
	if err != nil {
		return ProgramOutcome{}, err
	}
	out := ProgramOutcome{ProgramID: run.root.Identity.ProgramID, State: projection.Program.State, Status: result.Status, StopCode: result.StopCode, Truncated: stdout.truncated || stderr.truncated,
		ExecutedChildren: projection.ExecutedChildren, CompletedEffects: projection.CompletedEffects, UncertainEffects: projection.UncertainEffects}
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
	outputs, err := invocation.NewProgramOutputRetention(ctx, r.Recorder, r.Blobs, sessionID, r.sessionResources())
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
