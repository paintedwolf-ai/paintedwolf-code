// Package mutation is the command frontend for redirects, tee and filesystem
// mutations. Each utility binds argv through the catalog and calls the same
// typed write, delete, move, copy, mkdir and chmod operations as the structured
// tools, so scope, review, attribution, the source journal and recovery are
// the existing owners'. Redirect publication captures every destination
// before its producer starts and publishes only at that captured revision.
package mutation

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// PinRecorder retains the revisions one execution captured
// (invocation.SQLRecorder.AppendRevisionPin). Each capture appends at the
// execution's next position, so redirects and the utility's own captures in
// one execution never contend for an ordinal.
type PinRecorder interface {
	AppendRevisionPin(ctx context.Context, executionID string, pin toolcontract.ExecutionRevision) error
}

// Operations are the shared typed mutation owners the utilities call.
type Operations struct {
	Write   *native.WriteTool
	Delete  *native.DeleteTool
	Move    *native.MoveTool
	Copy    *native.CopyTool
	Mkdir   *native.MkdirTool
	Chmod   *native.ChmodTool
	Chown   *native.ChownTool
	Times   *native.TimesTool
	Extract *native.ExtractArchiveTool
}

// Owners holds the operations behind every mutation utility. One value serves
// many programs; per-invocation state lives in Prepared and Publication.
type Owners struct {
	Revisions  native.ExecutionRevisions
	Pins       PinRecorder
	Operations Operations
	Admission  tooloutput.SpillAdmission

	catalog toolcontract.LanguageCatalog
	specs   map[string]toolcontract.CommandSpecification
	ops     map[string]string
}

type utility func(*invocation) error

var utilities = map[string]utility{
	"rm": runRm, "rmdir": runRmdir, "mv": runMv, "cp": runCp, "ln": runLn, "mkdir": runMkdir,
	"touch": runTouch, "chmod": runChmod, "chown": runChown, "tee": runTee, "mktemp": runMktemp,
	"tar": runTar, "zip": runZip, "unzip": runUnzip, "gzip": runGzip, "gunzip": runGunzip, "zcat": runZcat,
	"edit": runEdit, "replace-lines": runReplaceLines, "code-rewrite": runCodeRewrite, "patch": runPatch,
}

// createOperations names the owner of utilities whose catalog entry has no
// structured tool disposition: they create and replace files through the
// write operation.
var createOperations = map[string]string{"touch": "write", "mktemp": "write", "patch": "write"}

// NewOwners binds the frozen catalog's specifications and operation identities.
func NewOwners(revisions native.ExecutionRevisions, pins PinRecorder, operations Operations) (*Owners, error) {
	if operations.Write == nil || operations.Delete == nil || operations.Move == nil || operations.Copy == nil || operations.Mkdir == nil ||
		operations.Chmod == nil || operations.Chown == nil || operations.Times == nil || operations.Extract == nil || pins == nil {
		return nil, errors.New("mutation owners require every shared operation and a pin recorder")
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{Revisions: revisions, Pins: pins, Operations: operations, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, ops: map[string]string{}}
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
	for name, op := range createOperations {
		o.ops[name] = op
	}
	for name := range utilities {
		if _, ok := o.specs[name]; !ok || o.ops[name] == "" {
			return nil, fmt.Errorf("mutation utility %s has no catalog command or operation identity", name)
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
	for _, limit := range o.catalog.Language.Limits {
		if limit.ID == id {
			return limit.Maximum
		}
	}
	return 0
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

// Program is one invocation's logical cwd.
type Program struct {
	Paths *projectpaths.ProgramPaths
}

// Prepare binds a reached command through the catalog option grammar. It
// reads and writes nothing. The request names the catalog command.
func (o *Owners) Prepare(program Program, request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	command := request.Argv[0].Value
	spec, ok := o.specs[command]
	if !ok || program.Paths == nil {
		return Prepared{}, fmt.Errorf("%s is not a mutation command", command)
	}
	if request.OperationID != command {
		return Prepared{}, fmt.Errorf("request names operation %s, not command %s", request.OperationID, command)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
	if err != nil {
		return Prepared{}, argvRejection(err, spec)
	}
	review := bound.Review(o.ops[command])
	review.PathTargets = mutationPathTargets(bound)
	return Prepared{Operation: o.ops[command], Mode: mutationMode(spec, bound), Argv: bound, Review: review, owners: o, program: program, request: request, spec: spec, run: utilities[command]}, nil
}

func mutationMode(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) string {
	hasOpt := func(key string) bool {
		_, ok := bound.Last(key)
		return ok
	}
	switch spec.Name {
	case "tar":
		if hasOpt("-t") {
			return "listing-or-stream-decode"
		}
		return "create-extract-compress-decompress-to-path"
	case "zip", "unzip":
		if hasOpt("-l") {
			return "listing-or-stream-decode"
		}
		return "create-extract-compress-decompress-to-path"
	case "gzip":
		if hasOpt("-t") || hasOpt("-c") {
			return "listing-or-stream-decode"
		}
		return "create-extract-compress-decompress-to-path"
	case "gunzip":
		if hasOpt("-c") || len(bound.Operands()) == 0 {
			return "listing-or-stream-decode"
		}
		return "create-extract-compress-decompress-to-path"
	case "code-rewrite", "patch":
		if hasOpt("--dry-run") {
			return "dry-run"
		}
		return "mutation"
	}
	if len(spec.Effects) > 0 {
		return spec.Effects[0].Mode
	}
	return ""
}

// InvokeOwner runs the bound utility once and settles its producer outcome.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if p.run == nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, fmt.Errorf("mutation invocation is not prepared")
	}
	if p.Argv.Help {
		if streams.Input != nil {
			_ = streams.Input.Close()
		}
		return commandprogram.WriteCatalogHelp(p.spec, streams.Output)
	}
	inv := newInvocation(ctx, p, tc, streams)
	err := p.run(inv)
	if err == nil {
		err = ctx.Err()
	}
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	return inv.settle(err)
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
