// Package textutil is the command frontend for byte text transforms and
// argument batching: echo, printf, seq, yes, sort, uniq, cut, tr, paste,
// join, comm, column, fold, tac, rev, sed, awk, iconv, envsubst and xargs.
// Every utility binds argv through the catalog, streams exact bytes through
// commandprogram.Streams with no ambient locale, and charges working memory
// to the program. xargs runs each batch as its own child through the
// evaluator's nested-command port; input never becomes program text.
package textutil

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// PinRecorder retains the revisions one execution captured
// (invocation.SQLRecorder.AppendRevisionPin). Each capture appends at the
// execution's next position, after the program's redirects and other owners.
type PinRecorder interface {
	AppendRevisionPin(ctx context.Context, executionID string, pin toolcontract.ExecutionRevision) error
}

// Owners serves every program; per-invocation state lives in Prepared.
type Owners struct {
	// Revisions captures and pins every file operand before it is read.
	Revisions native.ExecutionRevisions
	Pins      PinRecorder
	// Regex is the shared POSIX BRE/ERE engine sed compiles with. Without
	// it sed refuses rather than run on another engine.
	Regex PosixRegexEngine

	catalog toolcontract.LanguageCatalog
	specs   map[string]toolcontract.CommandSpecification
	limits  map[string]int64
}

type utility func(*invocation) error

var utilities = map[string]utility{
	"echo": runEcho, "printf": runPrintf, "seq": runSeq, "yes": runYes,
	"sort": runSort, "uniq": runUniq, "cut": runCut, "tr": runTr,
	"paste": runPaste, "join": runJoin, "comm": runComm, "column": runColumn,
	"fold": runFold, "tac": runTac, "rev": runRev,
	"xargs": runXargs, "sed": runSed, "awk": runAwk,
	"iconv": runIconv, "envsubst": runEnvsubst,
}

// leadingOptions are utilities whose options are only the declared leading
// words; every later word, dash-prefixed or not, is data. The value reports
// whether "--" ends the options (printf) or is data (echo).
var leadingOptions = map[string]bool{"echo": false, "printf": true}

// NewOwners binds the frozen catalog's specifications and ceilings.
func NewOwners(revisions native.ExecutionRevisions, pins PinRecorder) (*Owners, error) {
	if pins == nil {
		return nil, errors.New("text utilities require a revision pin recorder")
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	engine, err := textsearch.NewPOSIXEngine()
	if err != nil {
		return nil, err
	}
	o := &Owners{Revisions: revisions, Pins: pins, Regex: engine, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, limits: map[string]int64{}}
	for _, command := range catalog.Commands {
		if _, ok := utilities[command.Name]; ok {
			o.specs[command.Name] = command
		}
	}
	for name := range utilities {
		if _, ok := o.specs[name]; !ok {
			return nil, fmt.Errorf("text utility %s is not in the language catalog", name)
		}
	}
	for _, limit := range catalog.Language.Limits {
		o.limits[limit.ID] = limit.Maximum
	}
	for _, id := range []string{"stream_throughput_bytes", "whole_file_bytes", "stream_bytes", "record_bytes", "sort_memory_bytes", "sort_merge_fan_in", "spill_bytes", "argv_bytes", "argument_bytes", "expanded_fields", "child_executions", "query_work_steps", "evaluation_memory_bytes"} {
		if o.limits[id] < 1 {
			return nil, fmt.Errorf("language catalog is missing %s", id)
		}
	}
	return o, nil
}

// Commands lists the catalog commands these owners serve, in catalog order.
func (o *Owners) Commands() []string {
	out := make([]string, 0, len(o.specs))
	for _, command := range o.catalog.Commands {
		if _, ok := o.specs[command.Name]; ok {
			out = append(out, command.Name)
		}
	}
	return out
}

// Ports are one program's resources for these utilities. Memory is the
// program's aggregate working memory; Spills holds sort runs, which are
// reserved against Admission before any byte is written; Nested runs each
// xargs batch as its own child.
type Ports struct {
	ToolContext tools.ToolContext
	Memory      *toolcontract.ProgramMemory
	Spills      SpillStorage
	Admission   tooloutput.SpillAdmission
	Nested      commandprogram.NestedCommand
	// Publication is the program's redirect owner (mutation.Publication); sed
	// -i files and uniq OUTPUT stage and publish through it.
	Publication commandprogram.OutputPublication
}

// Builtins returns one in-process dispatcher per utility for one program.
func (o *Owners) Builtins(ports Ports) map[string]commandprogram.OperationDispatcher {
	out := make(map[string]commandprogram.OperationDispatcher, len(o.specs))
	for name := range o.specs {
		out[name] = dispatcher{owners: o, ports: ports}
	}
	return out
}

type dispatcher struct {
	owners *Owners
	ports  Ports
}

func (d dispatcher) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	owners := *d.owners
	if d.ports.Memory != nil {
		owners.Revisions.Memory = d.ports.Memory
	}
	prepared, err := owners.Prepare(ctx, request)
	if err != nil {
		err = tools.CommandResourceRejection(err)
		return nativeutility.Settle(err, 0, false), err
	}
	defer prepared.Close()
	return prepared.InvokeOwner(ctx, d.ports, streams)
}

func (d dispatcher) EffectMode(ctx context.Context, request commandprogram.OperationRequest) string {
	owners := *d.owners
	if d.ports.Memory != nil {
		owners.Revisions.Memory = d.ports.Memory
	}
	prepared, err := owners.Prepare(ctx, request)
	if err != nil {
		return ""
	}
	defer prepared.Close()
	return prepared.Mode
}

// Prepared is one bound utility invocation; it reads no input until run.
// Mode is the catalog effect mode the bound form selects, or empty when the
// command has one mode.
type Prepared struct {
	Argv           commandprogram.BoundArgv
	Review         commandprogram.ArgvReview
	Mode           string
	DeferredSource bool
	owners         *Owners
	request        commandprogram.OperationRequest
	spec           toolcontract.CommandSpecification
	run            utility
	sed            *preparedSed
}

// Close releases any admitted resources retained by the prepared invocation.
func (p Prepared) Close() {
	p.sed.close()
}

// RecordSourceRead records a source revision after its own path was admitted,
// even if approval for the parsed script's effects is later declined.
func (p Prepared) RecordSourceRead(ctx context.Context) error {
	if p.sed == nil || p.sed.pinsRecorded {
		return nil
	}
	for _, pin := range p.sed.pins {
		if err := p.owners.Pins.AppendRevisionPin(ctx, p.request.Invocation.Identity.ExecutionID, pin); err != nil {
			return err
		}
	}
	p.sed.pinsRecorded = true
	return nil
}

// Prepare binds a reached command. The operation identity is the command name.
func (o *Owners) Prepare(ctx context.Context, request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	command := request.Argv[0].Value
	spec, ok := o.specs[command]
	if !ok {
		return Prepared{}, fmt.Errorf("%s is not a text utility", command)
	}
	if request.OperationID != spec.Name {
		return Prepared{}, fmt.Errorf("%s belongs to operation %s, not %s", command, spec.Name, request.OperationID)
	}
	var bound commandprogram.BoundArgv
	var err error
	if doubleDash, ok := leadingOptions[command]; ok {
		bound = leadingBind(spec, request.Argv, doubleDash)
	} else if bound, err = (commandprogram.CatalogBinder{}).Bind(spec, numericOperands(command, request.Argv)); err != nil {
		return Prepared{}, nativeutility.ArgvRejection(err, spec)
	}
	prepared := Prepared{Argv: bound, Review: bound.Review(spec.Name), owners: o, request: request, spec: spec, run: utilities[command]}
	if command == "sed" {
		sedPlan, mode, err := o.prepareSed(ctx, spec, request, bound)
		if err != nil {
			return Prepared{}, tools.CommandResourceRejection(err)
		}
		prepared.sed = sedPlan
		prepared.Mode = mode
		prepared.Review.PathTargets = o.pathTargets(bound, sedPlan)
		return prepared, nil
	}
	prepared.Mode = o.effectMode(bound)
	if PathUtility(command) {
		prepared.Review.PathTargets = o.pathTargets(bound, nil)
	}
	return prepared, nil
}

// PrepareNative projects script-file effects before the child approval while
// preserving the streaming invocation's source capture and execution pin.
func (o *Owners) PrepareNative(ctx context.Context, frame *projectpaths.ProgramPaths, ports Ports, request commandprogram.OperationRequest) (Prepared, error) {
	p, err := o.Prepare(ctx, request)
	if err != nil || p.Argv.Command != "sed" || !argvFlag(p.Argv, "-f") {
		return p, err
	}
	for _, item := range p.Argv.Items {
		if item.Option == nil || item.Option.Key != "-f" {
			continue
		}
		for _, value := range item.Option.Values {
			if _, err := frame.Bind(ctx, value.Value, false); err != nil {
				// The bound -f operand is already a typed read target. Defer
				// loading until that target has passed child admission.
				p.DeferredSource = true
				//nolint:nilerr // Deferred -f source loading validates the path during child admission.
				return p, nil
			}
		}
	}
	ports.ToolContext = frame.ToolContext()
	inv := newInvocation(ctx, p, ports, commandprogram.Streams{})
	inv.preparing = true
	source, program, err := loadSedFileScript(inv, p.sed.source)
	inv.close()
	if err != nil {
		p.Close()
		return Prepared{}, err
	}
	p.sed.source, p.sed.program = source, program
	p.Review.PathTargets = o.pathTargets(p.Argv, p.sed)
	if len(program.WriteFiles()) > 0 && p.Mode == "stream-script" {
		p.Mode = "w-command"
	}
	return p, nil
}

// leadingBind binds only leading words made entirely of declared boolean
// options (echo -ne) and, when doubleDash, an optional "--"; the rest are
// operands.
func leadingBind(spec toolcontract.CommandSpecification, argv commandprogram.Fields, doubleDash bool) commandprogram.BoundArgv {
	bound := commandprogram.BoundArgv{Command: spec.Name}
	declared := map[string]string{}
	for _, form := range spec.Forms {
		for _, spelling := range form.Spellings {
			declared[spelling] = form.Spellings[0]
		}
	}
	index := 1
	for ; index < len(argv); index++ {
		word := argv[index]
		if word.Value == "--" && doubleDash {
			bound.EndOfOptions = true
			index++
			break
		}
		if word.Value == "--help" {
			bound.Help = true
			bound.Items = append(bound.Items, commandprogram.BoundItem{Option: &commandprogram.BoundOption{Key: "--help", Spelling: "--help", ValueRole: "boolean", Span: word.Span}})
			continue
		}
		keys, ok := shortCluster(word.Value, declared)
		if !ok {
			break
		}
		for _, key := range keys {
			bound.Items = append(bound.Items, commandprogram.BoundItem{Option: &commandprogram.BoundOption{Key: key, Spelling: key, ValueRole: "boolean", Span: word.Span}})
		}
	}
	for ; index < len(argv); index++ {
		operand := argv[index]
		bound.Items = append(bound.Items, commandprogram.BoundItem{Operand: &operand})
	}
	return bound
}

func shortCluster(word string, declared map[string]string) ([]string, bool) {
	if len(word) < 2 || word[0] != '-' {
		return nil, false
	}
	var keys []string
	for i := 1; i < len(word); i++ {
		key, ok := declared["-"+word[i:i+1]]
		if !ok {
			return nil, false
		}
		keys = append(keys, key)
	}
	return keys, true
}

// InvokeOwner runs the bound utility once and settles its producer outcome.
func (p Prepared) InvokeOwner(ctx context.Context, ports Ports, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	defer p.Close()
	if p.run == nil {
		err := errors.New("text utility invocation is not prepared")
		return nativeutility.Settle(err, 0, false), err
	}
	if ports.Memory == nil {
		err := errors.New("text utilities require program memory admission")
		return nativeutility.Settle(err, 0, false), err
	}
	if p.Argv.Help {
		if streams.Input != nil {
			_ = streams.Input.Close()
		}
		return commandprogram.WriteCatalogHelp(p.spec, streams.Output)
	}
	inv := newInvocation(ctx, p, ports, streams)
	err := p.run(inv)
	if err == nil {
		err = inv.out.Flush()
	}
	if err == nil && inv.stoppedBatch == nil {
		err = ctx.Err()
	}
	inv.close()
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	if inv.stoppedBatch != nil {
		return *inv.stoppedBatch, err
	}
	if inv.errors.Len() > 0 && streams.Error != nil {
		_, _ = io.WriteString(streams.Error, inv.errors.String())
	}
	var domain toolcontract.ProducerFailure
	if errors.As(err, &domain) {
		return domain.ProducerResult(), err
	}
	err = tools.CommandResourceRejection(err)
	result := nativeutility.Settle(err, inv.status, false)
	if (p.Mode == "-i" || p.Mode == "w-files" || p.Mode == "w-command" || p.Mode == "-o" || p.Mode == "OUTPUT-supplied") && result.Terminal == toolcontract.ProducerCompleted && inv.status == 0 {
		result.EffectCertainty = toolcontract.EffectCompleted
	}
	if inv.uncertain {
		result.EffectCertainty = toolcontract.EffectUnknown
	} else if inv.published {
		result.EffectCertainty = toolcontract.EffectCompleted
	}
	return result, err
}
