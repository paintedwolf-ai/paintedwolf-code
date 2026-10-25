package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

var _ commandprogram.NestedCommand = (*ProgramDispatcher)(nil)

// RunNested runs a utility's command operand (env COMMAND) as its own child
// of the parent statement, with exactly the given environment, through the
// same admission and receipts as any reached command.
func (d *ProgramDispatcher) RunNested(ctx context.Context, parent commandprogram.OperationRequest, argv commandprogram.Fields, environment map[string]string, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if len(argv) == 0 {
		return refusedResult("COMMAND_OPERAND_INVALID"), fmt.Errorf("nested command requires a program")
	}
	streams.Output = d.syncWriter(streams.Output)
	streams.Error = d.syncWriter(streams.Error)
	entries := make([]string, 0, len(environment))
	for name, value := range environment {
		entries = append(entries, name+"="+value)
	}
	sort.Strings(entries)
	child := commandprogram.NextExecution(parent.Invocation, parent.Invocation.Identity.NodeID, parent.Invocation.Span)
	child.Scope = parent.Invocation.Scope
	request := commandprogram.OperationRequest{Invocation: child, OperationID: argv[0].Value, Statement: parent.Statement, Argv: argv, Environment: entries}
	spec, native := d.run.commands[argv[0].Value]
	if !native || spec.Name == "external" {
		request.OperationID = "external"
		return d.Launch(ctx, request, streams)
	}
	request.OperationID = spec.Name
	if !leadingOptionCommand(spec.Name) {
		if _, err := d.binder.Bind(spec, argv); err != nil {
			if gap := unsupportedForm(spec, err); gap != nil && d.GapLaunchable(request) {
				request.Gap = gap
				return d.Launch(ctx, request, streams)
			}
			return d.refuseArgv(ctx, spec, request, err, streams)
		}
	}
	return d.Invoke(ctx, request, streams)
}

func leadingOptionCommand(name string) bool {
	return name == "echo" || name == "printf" || name == "ps" || name == "jobs"
}

func (d *ProgramDispatcher) syncWriter(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &lockedWriter{mu: &d.shared.mu, w: w}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

var (
	_ commandprogram.ExecutionRecorder   = (*ProgramDispatcher)(nil)
	_ commandprogram.SourceUnitRegistrar = (*ProgramDispatcher)(nil)
)

// RegisterSourceUnit records a reached sourced unit and its static nodes in the
// program ledger and retains its source text for child span display.
func (d *ProgramDispatcher) RegisterSourceUnit(ctx context.Context, _ toolcontract.OperationInvocation, source commandprogram.CapturedSource, plan commandsurface.ProgramSnapshot) error {
	unitID := source.Pin.SourceID
	d.run.setSourceText(unitID, string(source.Bytes))
	if d.run.recorder == nil {
		return nil
	}
	locations := programNodes(plan.Statements)
	nodes := make([]toolcontract.ProgramNode, 0, len(locations))
	for _, loc := range locations {
		nodes = append(nodes, toolcontract.ProgramNode{ID: string(loc.ID), SourceUnitID: unitID, Span: loc.Span})
	}
	sourceRef := "output:program-source:" + d.run.root.Identity.ProgramID
	return d.run.recorder.RegisterSourceUnit(ctx, d.run.root.Identity.ProgramID, toolcontract.ProgramSource{UnitID: unitID, Pin: source.Pin, DetailReference: sourceRef}, nodes)
}

// BeginExecution records an execution the evaluator runs itself. A catalog
// builtin (true, test, [[, export) also commits its owner-start marker; a
// structural execution (pipeline, group, function, substitution, job,
// redirection) owns no effect of its own.
func (d *ProgramDispatcher) BeginExecution(ctx context.Context, invocation toolcontract.OperationInvocation, command string) error {
	spec, native := d.run.commands[command]
	definition, err := d.structuralDefinition(command)
	if native {
		definition, err = d.catalogDefinition(spec)
	}
	if err != nil {
		return err
	}
	display := d.run.sourceText(invocation.SourceUnitID)
	if span := invocation.Span; span.StartByte >= 0 && span.EndByte <= len(display) && span.StartByte <= span.EndByte {
		display = display[span.StartByte:span.EndByte]
	}
	child, err := d.prepareInvocation(ctx, definition, invocation, display, map[string]any{"operation_id": command}, nil)
	if err != nil {
		return err
	}
	invoked := false
	if native && len(spec.Effects) > 0 {
		if child.bound, err = toolcontract.BindOperation(definition, invocation, spec.Name, spec.Effects[0].Mode, nil); err == nil {
			err = d.start(ctx, child)
		}
		if err != nil {
			_ = d.settle(ctx, child, ownerFailure(err), false, "")
			child.release()
			return err
		}
		invoked = true
	} else if isRedirectConstruct(command) {
		if teeSpec, ok := d.run.commands["tee"]; ok && len(teeSpec.Effects) > 0 {
			child.bound, _ = toolcontract.BindOperation(definition, invocation, teeSpec.Name, teeSpec.Effects[0].Mode, nil)
		}
	}
	d.shared.mu.Lock()
	if d.shared.evaluated == nil {
		d.shared.evaluated = map[string]evaluatedChild{}
	}
	d.shared.evaluated[invocation.Identity.ExecutionID] = evaluatedChild{child: child, ownerInvoked: invoked}
	d.shared.mu.Unlock()
	return nil
}

// SettleExecution settles a recorded execution once with the evaluator's record.
func (d *ProgramDispatcher) SettleExecution(ctx context.Context, invocation toolcontract.OperationInvocation, record commandprogram.ChildRecord) error {
	d.shared.mu.Lock()
	entry, ok := d.shared.evaluated[invocation.Identity.ExecutionID]
	delete(d.shared.evaluated, invocation.Identity.ExecutionID)
	d.shared.mu.Unlock()
	if !ok {
		return fmt.Errorf("execution %s was not recorded", invocation.Identity.ExecutionID)
	}
	defer entry.child.release()
	result := record.Result
	if !record.Executed || result.Validate() != nil {
		result = toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}
	}
	if !entry.ownerInvoked && result.EffectCertainty == toolcontract.EffectCompleted && isRedirectConstruct(record.Command) {
		if err := d.start(ctx, entry.child); err == nil {
			entry.ownerInvoked = true
		}
	}
	if !entry.ownerInvoked && result.EffectCertainty == toolcontract.EffectCompleted {
		// Descendants own their effects; a structural node only aggregates them.
		result.EffectCertainty = toolcontract.EffectUnknown
	}
	return d.settleWith(ctx, entry.child, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: entry.ownerInvoked, JobHandle: record.JobHandle, Conflict: revisionConflict(record.Diagnostic)})
}

func isRedirectConstruct(command string) bool {
	switch command {
	case ">", ">>", "2>", "2>>", "&>", "&>>":
		return true
	default:
		return strings.HasPrefix(command, "redirect ")
	}
}

// structuralDefinition compiles one definition per evaluator construct.
func (d *ProgramDispatcher) structuralDefinition(construct string) (toolcontract.OperationDefinition, error) {
	d.shared.mu.Lock()
	defer d.shared.mu.Unlock()
	key := "construct " + construct
	if definition, ok := d.shared.definitions[key]; ok {
		return definition, nil
	}
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		return toolcontract.OperationDefinition{}, fmt.Errorf("command contract missing")
	}
	var commands []toolcontract.CommandSpecification
	if isRedirectConstruct(construct) {
		if teeSpec, ok := d.run.commands["tee"]; ok {
			contract.Owner = teeSpec.Owner
			commands = []toolcontract.CommandSpecification{teeSpec}
		}
	}
	definition, err := toolcontract.NewOperationDefinition(toolcontract.OperationFacts{
		Identity: "program " + construct, Version: 1, Contract: contract, Arguments: json.RawMessage(`{"type":"object"}`),
		Commands: commands, Resources: d.run.catalog.Language.Limits, ProgressGate: toolcontract.ProgressNotRequired, RejectionContract: "command",
		Streams: toolcontract.StreamContract{Input: "program streams", Output: "program streams", Error: "typed owner rejection; prose is diagnostic", Statuses: map[int]string{0: "aggregate status of the construct"}},
	})
	if err != nil {
		return toolcontract.OperationDefinition{}, err
	}
	if d.shared.definitions == nil {
		d.shared.definitions = map[string]toolcontract.OperationDefinition{}
	}
	d.shared.definitions[key] = definition
	return definition, nil
}

var _ commandprogram.FrameRedirections = (*ProgramDispatcher)(nil)

// Redirections binds redirect targets from this frame's current directory.
func (d *ProgramDispatcher) Redirections() (commandprogram.InputRedirection, commandprogram.OutputPublication) {
	if d.redirections == nil {
		return nil, nil
	}
	return d.redirections(d.paths)
}

// NativeBindings reports whether a program binds a name to a native owner.
type NativeBindings interface {
	NativeBound(name string) bool
}

// NativeBound reports whether dispatch reaches a native owner for name:
// evaluator built-ins and every catalog command with a wired owner.
func (d *ProgramDispatcher) NativeBound(name string) bool {
	if commandprogram.EvaluatorBuiltin(name) {
		return true
	}
	spec, native := d.run.commands[name]
	if !native || spec.Name == "external" {
		return spec.Name == "external"
	}
	return d.natives[spec.Name].Prepare != nil || pathCommands[spec.Name] || d.builtins[spec.Name] != nil
}

// GapLaunchable reports whether a native name may run as its installed
// program: a request that names one, found by the launch's own lookup.
func (d *ProgramDispatcher) GapLaunchable(request commandprogram.OperationRequest) bool {
	if len(request.Argv) == 0 || request.Argv[0].Value == "external" {
		return false
	}
	return (ExternalStages{ToolContext: d.run.tc, Dir: d.paths.Cwd().Abs}).Installed(request.Argv[0].Value, request.Environment)
}

// unsupportedForm is the native gap a typed unsupported-form refusal names;
// other refusals (operand errors, policy) are not gaps and never fall back.
func unsupportedForm(spec toolcontract.CommandSpecification, cause error) *toolcontract.NativeGap {
	code, form := "", ""
	var argv *commandprogram.ArgvError
	if errors.As(cause, &argv) {
		code, form = argv.Code, argv.Word
	} else if reject := toolrejection.AsToolReject(cause); reject != nil {
		code = reject.Code
		form, _ = reject.Data["word"].(string)
	}
	if code != "COMMAND_OPTION_UNSUPPORTED" && code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		return nil
	}
	return &toolcontract.NativeGap{Command: spec.Name, Code: code, Form: form}
}

// writeHostRefusal adds the host's refusal to the stopped statement's stderr
// in shell voice beside the structured refusal.
func writeHostRefusal(streams commandprogram.Streams, command, form, code string) {
	if streams.Error == nil {
		return
	}
	subject := command
	if form != "" {
		subject += ": " + form
	}
	_, _ = fmt.Fprintf(streams.Error, "%s: not supported here [%s]\n", subject, code)
}

type trackingWriter struct {
	w     io.Writer
	wrote bool
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		t.wrote = true
	}
	return t.w.Write(p)
}

func writeRejectRefusal(streams commandprogram.Streams, command string, request commandprogram.OperationRequest, reject *toolrejection.ToolReject) {
	if streams.Error == nil || reject == nil {
		return
	}
	subject := command
	for _, key := range []string{"word", "path", "operand", "job", "signal", "pattern"} {
		if val, _ := reject.Data[key].(string); val != "" {
			subject += ": " + val
			break
		}
	}
	if subject == command && len(request.Argv) > 1 {
		subject += ": " + request.Argv[1].Value
	}
	detail := ""
	for _, key := range []string{"expected", "reason", "remedy", "expected_form"} {
		if val, _ := reject.Data[key].(string); val != "" {
			detail = val
			break
		}
	}
	switch reject.Code {
	case "SURVEY_PATH_ESCAPE":
		detail = "path is outside the project root; use a relative path inside the workspace"
	case "GREP_REGEX_INVALID":
		if detail == "" {
			detail = "invalid regular expression; use -F for fixed strings or escape regex metacharacters"
		}
	case "COMMAND_JOB_UNKNOWN":
		detail = "no such job in this session; check active jobs with jobs"
	case "COMMAND_OPTION_UNSUPPORTED", "COMMAND_NATIVE_FORM_UNSUPPORTED":
		if detail != "" {
			detail = "not supported here (expected " + detail + ")"
		} else {
			detail = "not supported here"
		}
	}
	if detail == "" {
		detail = "not supported here"
	}
	_, _ = fmt.Fprintf(streams.Error, "%s: %s [%s]\n", subject, detail, reject.Code)
}

func definitionHasCommand(definition toolcontract.OperationDefinition, name string) bool {
	for _, cmd := range definition.Facts().Commands {
		if cmd.Name == name {
			return true
		}
	}
	return false
}

func selectEffectMode(spec toolcontract.CommandSpecification, requested string) string {
	for _, effect := range spec.Effects {
		if effect.Mode == requested && requested != "" {
			return requested
		}
	}
	if len(spec.Effects) > 0 {
		return spec.Effects[0].Mode
	}
	return ""
}

func programOperationDefinition(definition toolcontract.OperationDefinition) toolcontract.OperationDefinition {
	facts := definition.Facts()
	if facts.ProgressGate == toolcontract.ProgressNotRequired {
		return definition
	}
	facts.ProgressGate = toolcontract.ProgressNotRequired
	updated, err := toolcontract.NewOperationDefinition(facts)
	if err != nil {
		return definition
	}
	return updated
}

func (d *ProgramDispatcher) childEffectSet(spec toolcontract.CommandSpecification, mode string, request commandprogram.OperationRequest) *toolcontract.CommandEffects {
	requiresBound := false
	for _, effect := range spec.Effects {
		if effect.Mode == mode && slices.Contains(effect.Requires, "bound-child-effect-set") {
			requiresBound = true
			break
		}
	}
	if !requiresBound {
		return nil
	}
	effects := d.boundChildEffects(spec, request)
	return &effects
}

func (d *ProgramDispatcher) boundChildEffects(spec toolcontract.CommandSpecification, request commandprogram.OperationRequest) toolcontract.CommandEffects {
	readonly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	var childArgv []string
	switch spec.Name {
	case "xargs":
		if bound, err := d.binder.Bind(spec, request.Argv); err == nil {
			childArgv = bound.Operands().Values()
		}
		if len(childArgv) == 0 {
			return readonly
		}
	case "env":
		if bound, err := d.binder.Bind(spec, request.Argv); err == nil {
			for i, op := range bound.Operands() {
				if idx := strings.IndexByte(op.Value, '='); idx <= 0 {
					childArgv = bound.Operands()[i:].Values()
					break
				}
			}
		}
		if len(childArgv) == 0 {
			return readonly
		}
	case "find":
		values := request.Argv.Values()
		for i := 1; i < len(values); i++ {
			if values[i] == "-exec" && i+1 < len(values) {
				childArgv = values[i+1:]
				break
			}
		}
		if len(childArgv) == 0 {
			return readonly
		}
	default:
		return readonly
	}
	childSpec, ok := d.run.commands[childArgv[0]]
	if !ok || childSpec.Name == "external" {
		ext, _ := toolcontract.NewCommandEffects(toolcontract.CommandExternalExec)
		return ext
	}
	if len(childSpec.Effects) == 0 {
		return readonly
	}
	effect := childSpec.Effects[0]
	if childSpec.Name == "sed" && slices.Contains(childArgv[1:], "-i") && len(childSpec.Effects) > 1 {
		effect = childSpec.Effects[1]
	}
	if slices.Contains(effect.Requires, "bound-child-effect-set") {
		return readonly
	}
	compiled, err := effect.CommandEffects(nil)
	if err != nil {
		return readonly
	}
	return compiled
}

// revisionConflict is a COMMAND_REVISION_CONFLICT refusal's typed facts: the
// destination path and its expected and observed revisions.
func revisionConflict(err error) *toolcontract.RevisionConflict {
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "COMMAND_REVISION_CONFLICT" {
		return nil
	}
	path, _ := reject.Data["path"].(string)
	expected, _ := reject.Data["expected_revision"].(string)
	observed, _ := reject.Data["observed_revision"].(string)
	if path == "" || expected == "" || observed == "" || expected == observed {
		return nil
	}
	return &toolcontract.RevisionConflict{Path: path, ExpectedRevision: expected, ObservedRevision: observed}
}

type renderedProgramRefusal struct {
	text  string
	cause error
}

func (r *renderedProgramRefusal) Error() string { return r.text }
func (r *renderedProgramRefusal) Unwrap() error { return r.cause }

func enrichProgramReject(reject *toolrejection.ToolReject, defaultCommand string, defaultSpan toolcontract.SourceSpan) {
	if reject == nil {
		return
	}
	if reject.Data == nil {
		reject.Data = map[string]any{}
	}
	if _, ok := reject.Data["tool"]; !ok {
		reject.Data["tool"] = "command"
	}
	cmd := firstStringFact(reject.Data, "command", "native_command", "operation")
	if cmd == "" {
		cmd = defaultCommand
	}
	if cmd == "" {
		cmd = "command"
	}
	reject.Data["command"] = cmd
	word := firstStringFact(reject.Data, "word", "path", "operand", "job", "signal", "pattern")
	if word != "" {
		reject.Data["word"] = word
	}
	line := firstIntFact(reject.Data, defaultSpan.Line, "source_line", "operation_source_line")
	col := firstIntFact(reject.Data, defaultSpan.Column, "source_column", "operation_source_column")
	if line < 1 {
		line = 1
	}
	if col < 1 {
		col = 1
	}
	reject.Data["source_line"] = line
	reject.Data["source_column"] = col
	if _, ok := reject.Data["source_start_byte"]; !ok {
		reject.Data["source_start_byte"] = defaultSpan.StartByte
	}
	if _, ok := reject.Data["source_end_byte"]; !ok {
		reject.Data["source_end_byte"] = defaultSpan.EndByte
	}
	remedy := firstStringFact(reject.Data, "expected_form", "expected", "remedy", "reason")
	switch reject.Code {
	case "SURVEY_PATH_ESCAPE":
		remedy = "path is outside the project root; use a relative path inside the workspace"
	case "GREP_REGEX_INVALID":
		remedy = "invalid regular expression; use -F for fixed strings or escape regex metacharacters"
	case "COMMAND_JOB_UNKNOWN":
		remedy = "no such job in this session; use jobs to list this session's background jobs, then pass %N, %% or $!"
	}
	if remedy == "" {
		remedy = "rewrite the command with a supported form"
	}
	if _, ok := reject.Data["expected_form"]; !ok {
		reject.Data["expected_form"] = remedy
	}
	reject.Data["reason"] = programRefusalLine(cmd, word, line, col, remedy)
}

func ensureProgramRefusalText(rendered error, reject *toolrejection.ToolReject) error {
	if reject == nil {
		return rendered
	}
	if rendered == nil {
		rendered = toolrejection.RenderReject(reject, nil)
	}
	text := rendered.Error()
	cmd := firstStringFact(reject.Data, "command", "native_command", "operation")
	word := firstStringFact(reject.Data, "word", "path", "operand", "job", "signal", "pattern")
	line := firstIntFact(reject.Data, 1, "source_line", "operation_source_line")
	col := firstIntFact(reject.Data, 1, "source_column", "operation_source_column")
	remedy := firstStringFact(reject.Data, "expected_form", "expected", "remedy", "reason")
	spanText := fmt.Sprintf("%d:%d", line, col)
	summary := programRefusalLine(cmd, word, line, col, remedy)
	if !strings.Contains(text, cmd) || (word != "" && !strings.Contains(text, word)) || !strings.Contains(text, spanText) || (remedy != "" && !strings.Contains(text, remedy)) {
		text = text + "\nStop: " + summary
	}
	return &renderedProgramRefusal{text: text, cause: rendered}
}

func programRefusalLine(cmd, word string, line, col int, remedy string) string {
	subject := cmd
	if word != "" {
		subject += " (" + word + ")"
	}
	return fmt.Sprintf("%s at %d:%d: %s", subject, line, col, remedy)
}

func firstStringFact(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if val, _ := data[key].(string); val != "" {
			return val
		}
	}
	return ""
}

func firstIntFact(data map[string]any, fallback int, keys ...string) int {
	for _, key := range keys {
		switch v := data[key].(type) {
		case int:
			if v > 0 {
				return v
			}
		case int64:
			if v > 0 {
				return int(v)
			}
		case float64:
			if v > 0 {
				return int(v)
			}
		}
	}
	return fallback
}
