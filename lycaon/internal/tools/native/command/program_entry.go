package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/bgprocess"
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
	Background *bgprocess.Registry
	Progress   toolexecution.ProgramProgressFacts
	Supervisor *ProgramSupervisor
	Evaluate   commandprogram.Evaluator
	Expand     commandprogram.Expander
	// Actor returns the host-attributed person for the invocation.
	Actor    func(tools.ToolContext) string
	Builtins func(tools.ToolContext) map[string]commandprogram.OperationDispatcher
	Natives  map[string]NativeOperation

	mu        sync.Mutex
	outputs   map[string]*invocation.ProgramOutputRetention
	resources *sessionOutputResources
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
	if r == nil || r.Executor == nil || r.Recorder == nil || r.Evaluate == nil || r.Supervisor == nil || r.Actor == nil {
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
	defer parsed.Close()
	defer snapshot.Close()

	root := toolcontract.OperationInvocation{
		Kind:         toolcontract.ProgramChildInvocation,
		Identity:     toolcontract.ExecutionIdentity{ProgramID: uuid.NewString(), NodeID: "program", ExecutionID: uuid.NewString()},
		ActorID:      r.Actor(tc),
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
	var active []toolcontract.OperationDefinition
	for _, operation := range r.Natives {
		if definition, ok := r.Registry.Definition(operation.Tool); ok {
			active = append(active, definition.Operation)
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
	builtins := map[string]commandprogram.OperationDispatcher{}
	if r.Builtins != nil {
		builtins = r.Builtins(tc)
	}
	jobs := &SessionJobs{Registry: r.Background, SessionID: tc.Identity.SessionID, RootSessionID: tc.ChatSessionID(), ProjectID: tc.Identity.ProjectID, ToolCallID: tc.Identity.ToolCallID}
	dispatcher := &ProgramDispatcher{run: run, builtins: builtins, natives: r.Natives, external: ExternalStages{ToolContext: tc}, binder: commandprogram.CatalogBinder{}}
	for name, builtin := range jobBuiltins(jobs, run.commands) {
		if _, taken := dispatcher.builtins[name]; !taken {
			dispatcher.builtins[name] = builtin
		}
	}
	stdout, stderr := &boundedBuffer{limit: programOutputBytes}, &boundedBuffer{limit: programOutputBytes}
	evaluation := commandprogram.Evaluation{
		Root: root, Plan: snapshot.Plan,
		Streams: commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr},
		Ports:   commandprogram.EvaluationPorts{Dispatch: dispatcher, Expand: r.Expand, Record: r.Recorder, Held: &ProgramHeldEffects{}, Jobs: jobs, Memory: memory},
	}
	result, evalErr := r.Supervisor.RunProgram(ctx, root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
		return r.Evaluate(ctx, evaluation)
	})
	settleErr := r.settleProgram(ctx, run, result)
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
	parser, err := commandsurface.NewProgramParser(ctx, memory)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := parser.Parse([]byte(source), commandsurface.ParseOptions{})
	closeErr := parser.Close()
	if err != nil {
		if reject := toolcommand.ProgramSyntaxObservation("command", err); reject != nil {
			return nil, nil, reject
		}
		return nil, nil, err
	}
	if closeErr != nil {
		_ = parsed.Close()
		return nil, nil, closeErr
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
	var collect func([]commandsurface.Statement)
	collect = func(statements []commandsurface.Statement) {
		for _, statement := range statements {
			nodes = append(nodes, toolcontract.ProgramNode{ID: string(statement.ID), SourceUnitID: "submitted", Span: statement.Span})
		}
	}
	collect(plan.Statements)
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

func (r *ProgramRuntime) settleProgram(ctx context.Context, run *programRun, result toolcontract.ProducerResult) error {
	state := "completed"
	switch result.Terminal {
	case toolcontract.ProducerRefused:
		state = "rejected"
	case toolcontract.ProducerInterrupted:
		state = "interrupted"
	case toolcontract.ProducerFailed, "":
		state = "failed"
	}
	status := result.Status
	return r.Recorder.SettleProgram(context.WithoutCancel(ctx), run.root.Identity.ProgramID, toolcontract.ProgramSettlement{State: state, Status: &status, StopCode: result.StopCode, DetailReference: "output:program-source:" + run.root.Identity.ProgramID})
}

// feedback renders the first native owner rejection through the program
// feedback boundary, with the program's completed and uncertain effects.
func (r *ProgramRuntime) feedback(ctx context.Context, run *programRun, dispatcher *ProgramDispatcher, evalErr error) error {
	rejection := dispatcher.firstRejection()
	if rejection == nil {
		if reject := toolrejection.AsToolReject(evalErr); reject != nil {
			return reject
		}
		return nil
	}
	prepared, err := toolexecution.PrepareProgramFailure(ctx, run.recorder, run.memory, run.tc, rejection.operation, rejection.cause)
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
