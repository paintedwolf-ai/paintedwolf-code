// Package query is the command frontend for jq, yq and the jq-edit built-in.
// jq streams complete values through the same compiled gojq program the
// structured jq tool evaluates; yq runs mikefarah yq v4's library; jq-edit
// runs the structured jq_edit's format-preserving edit. Edits publish at
// captured execution revisions.
package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// PinRecorder retains the revisions one execution captured
// (invocation.SQLRecorder.AppendRevisionPin). Pins append in capture order.
type PinRecorder interface {
	AppendRevisionPin(ctx context.Context, executionID string, pin toolcontract.ExecutionRevision) error
}

// Owners holds the shared operations behind jq, yq and jq-edit.
type Owners struct {
	Revisions native.ExecutionRevisions
	Pins      PinRecorder
	// Write is the shared write door jq-edit publishes through.
	Write *native.WriteTool

	catalog toolcontract.LanguageCatalog
	specs   map[string]toolcontract.CommandSpecification
	ops     map[string]string
	// limitOverride narrows catalog limits for bounded fixtures.
	limitOverride map[string]int64
}

type utility func(*invocation) error

var utilities = map[string]utility{"jq": runJq, "yq": runYq, "jq-edit": runJqEdit}

// NewOwners binds the frozen catalog's specifications and operation identities.
func NewOwners(revisions native.ExecutionRevisions, pins PinRecorder, write *native.WriteTool) (*Owners, error) {
	if pins == nil || write == nil {
		return nil, errors.New("query owners require a pin recorder and the write door")
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{Revisions: revisions, Pins: pins, Write: write, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, ops: map[string]string{}}
	for _, command := range catalog.Commands {
		if _, ok := utilities[command.Name]; ok {
			o.specs[command.Name] = command
		}
	}
	for _, disposition := range catalog.Tools {
		for _, form := range disposition.CommandForms {
			if _, ok := o.specs[form]; ok {
				o.ops[form] = disposition.Tool
			}
		}
	}
	for name := range utilities {
		if _, ok := o.specs[name]; !ok || o.ops[name] == "" {
			return nil, fmt.Errorf("query utility %s has no catalog command or operation identity", name)
		}
	}
	return o, nil
}

// Operation returns the catalog operation identity that owns a command.
func (o *Owners) Operation(command string) (string, bool) {
	op, ok := o.ops[command]
	return op, ok
}

// Commands lists every catalog command these owners serve, in catalog order.
func (o *Owners) Commands() []string {
	out := make([]string, 0, len(o.specs))
	for _, command := range o.catalog.Commands {
		if _, ok := o.specs[command.Name]; ok {
			out = append(out, command.Name)
		}
	}
	return out
}

func (o *Owners) limit(id string) int64 {
	if n, ok := o.limitOverride[id]; ok {
		return n
	}
	for _, limit := range o.catalog.Language.Limits {
		if limit.ID == id {
			return limit.Maximum
		}
	}
	return 0
}

// Program is one invocation's logical cwd.
type Program struct {
	Paths *projectpaths.ProgramPaths
}

// Prepared is one bound utility invocation. InvokeOwner matches
// toolexecution.PreparedNativeEffect: streams reach it only after admission.
type Prepared struct {
	Operation string
	Mode      string
	Argv      commandprogram.BoundArgv
	Review    commandprogram.ArgvReview
	owners    *Owners
	program   Program
	request   commandprogram.OperationRequest
	spec      toolcontract.CommandSpecification
	run       utility
}

// Prepare binds a reached command through the catalog option grammar. It
// reads and writes nothing.
func (o *Owners) Prepare(program Program, request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	command := request.Argv[0].Value
	spec, ok := o.specs[command]
	if !ok || program.Paths == nil {
		return Prepared{}, fmt.Errorf("%s is not a query command", command)
	}
	if request.OperationID != command {
		return Prepared{}, fmt.Errorf("request names operation %s, not command %s", request.OperationID, command)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
	if err != nil {
		return Prepared{}, argvRejection(err, spec)
	}
	mode := ""
	if len(spec.Effects) > 0 {
		mode = spec.Effects[0].Mode
	}
	if command == "yq" {
		if _, ok := bound.Last("-i"); ok {
			mode = "-i"
		} else {
			mode = "query"
		}
	}
	review := bound.Review(o.ops[command])
	review.PathTargets = queryPathTargets(bound, program.Paths)
	return Prepared{Operation: o.ops[command], Mode: mode, Argv: bound, Review: review, owners: o, program: program, request: request, spec: spec, run: utilities[command]}, nil
}

// InvokeOwner runs the bound utility once and settles its producer outcome.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if p.run == nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, fmt.Errorf("query invocation is not prepared")
	}
	inv := newInvocation(ctx, p, tc, streams)
	defer func() {
		for _, release := range inv.releases {
			release()
		}
	}()
	err := p.run(inv)
	if err == nil {
		err = ctx.Err()
	}
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	return inv.settle(err)
}

// Invoke prepares and runs one request, settling a binding refusal as the
// producer outcome.
func (o *Owners) Invoke(ctx context.Context, tc tools.ToolContext, program Program, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	//nolint:contextcheck // Query target preparation uses the bound ProgramPaths frame context.
	prepared, err := o.Prepare(program, request)
	if err != nil {
		result := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Terminal, result.StopCode = toolcontract.ProducerRefused, reject.Code
		}
		return result, err
	}
	return prepared.InvokeOwner(ctx, tc, streams)
}

// argvRejection maps a binder error to its catalog rejection.
func argvRejection(err error, spec toolcontract.CommandSpecification) error {
	var argvErr *commandprogram.ArgvError
	if !errors.As(err, &argvErr) {
		return err
	}
	return errors.Join(err, &toolrejection.ToolReject{Code: argvErr.Code, Data: map[string]any{
		"command": spec.Name, "word": argvErr.Word, "expected": argvErr.Expected, "expected_form": spec.Argv,
		"source_start_byte": argvErr.Span.StartByte, "source_end_byte": argvErr.Span.EndByte, "source_line": argvErr.Span.Line, "source_column": argvErr.Span.Column,
	}})
}
