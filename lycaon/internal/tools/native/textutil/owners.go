// Package textutil is the command frontend for byte text transforms and
// argument batching: echo, printf, sort, uniq, cut, tr and xargs. Every
// utility binds argv through the catalog, streams exact bytes through
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
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// PinRecorder retains the revisions one execution captured
// (invocation.SQLRecorder.RecordRevisionPins). Pins append in capture order.
type PinRecorder interface {
	RecordRevisionPins(ctx context.Context, executionID string, pins []toolcontract.ExecutionRevision) error
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
	"echo": runEcho, "printf": runPrintf, "sort": runSort, "uniq": runUniq,
	"cut": runCut, "tr": runTr, "xargs": runXargs, "sed": runSed, "awk": runAwk,
}

// leadingOptions are utilities whose options are only the declared leading
// words; every later word, dash-prefixed or not, is data.
var leadingOptions = map[string]bool{"echo": true, "printf": true}

// NewOwners binds the frozen catalog's specifications and ceilings.
func NewOwners(revisions native.ExecutionRevisions, pins PinRecorder) (*Owners, error) {
	if pins == nil {
		return nil, errors.New("text utilities require a revision pin recorder")
	}
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{Revisions: revisions, Pins: pins, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, limits: map[string]int64{}}
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
	for _, id := range []string{"stream_bytes", "record_bytes", "sort_memory_bytes", "sort_merge_fan_in", "spill_bytes", "argv_bytes", "argument_bytes", "expanded_fields", "child_executions", "query_work_steps", "evaluation_memory_bytes"} {
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
	// Write and Barrier publish sed -i files and uniq OUTPUT at their pins.
	Write   *native.WriteTool
	Barrier *native.PublicationBarrier
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
	prepared, err := d.owners.Prepare(request)
	if err != nil {
		return nativeutility.Settle(err, 0, false), err
	}
	return prepared.InvokeOwner(ctx, d.ports, streams)
}

// Prepared is one bound utility invocation; it reads no input until run.
type Prepared struct {
	Argv    commandprogram.BoundArgv
	Review  commandprogram.ArgvReview
	owners  *Owners
	request commandprogram.OperationRequest
	spec    toolcontract.CommandSpecification
	run     utility
}

// Prepare binds a reached command. The operation identity is the command name.
func (o *Owners) Prepare(request commandprogram.OperationRequest) (Prepared, error) {
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
	if leadingOptions[command] {
		bound = leadingBind(spec, request.Argv)
	} else if bound, err = (commandprogram.CatalogBinder{}).Bind(spec, request.Argv); err != nil {
		return Prepared{}, nativeutility.ArgvRejection(err, spec)
	}
	return Prepared{Argv: bound, Review: bound.Review(spec.Name), owners: o, request: request, spec: spec, run: utilities[command]}, nil
}

// leadingBind binds only leading words made entirely of declared boolean
// options (echo -ne) and an optional "--"; the rest are operands.
func leadingBind(spec toolcontract.CommandSpecification, argv commandprogram.Fields) commandprogram.BoundArgv {
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
		if word.Value == "--" {
			bound.EndOfOptions = true
			index++
			break
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
	if p.run == nil {
		err := errors.New("text utility invocation is not prepared")
		return nativeutility.Settle(err, 0, false), err
	}
	if ports.Memory == nil {
		err := errors.New("text utilities require program memory admission")
		return nativeutility.Settle(err, 0, false), err
	}
	inv := newInvocation(ctx, p, ports, streams)
	err := p.run(inv)
	if err == nil {
		err = ctx.Err()
	}
	inv.close()
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	var stopped *batchStop
	if errors.As(err, &stopped) {
		return stopped.result, err
	}
	if inv.errors.Len() > 0 && streams.Error != nil {
		_, _ = io.WriteString(streams.Error, inv.errors.String())
	}
	return nativeutility.Settle(err, inv.status, false), err
}
