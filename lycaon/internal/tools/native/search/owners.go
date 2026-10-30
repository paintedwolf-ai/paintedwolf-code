// Package search implements native search and discovery through held roots,
// shared directory observations, execution revisions, and textsearch.
package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// PinRecorder retains the revisions one execution captured
// (invocation.SQLRecorder.RecordRevisionPins). pins hold ordinals first,
// first+1, ... in capture order.
type PinRecorder interface {
	RecordRevisionPins(ctx context.Context, executionID string, first int, pins []toolcontract.ExecutionRevision) error
}

// Remover is the recoverable removal owner find -delete routes through. It
// journals one entry so the removal can be restored, refuses a non-empty
// directory, and never follows a final link.
type Remover interface {
	RemoveRecoverable(ctx context.Context, tc tools.ToolContext, target projectpaths.BoundPath) error
}

// DeleteRemover is the production Remover: the shared delete operation rm and
// rmdir also call, removing one entry (a file, link or empty directory)
// through its recoverable source journal.
type DeleteRemover struct{ Delete *native.DeleteTool }

func (d DeleteRemover) RemoveRecoverable(ctx context.Context, tc tools.ToolContext, target projectpaths.BoundPath) error {
	_, err := d.Delete.Delete(ctx, native.DeleteInput{Paths: []string{target.Resolved.Abs}}, tc)
	return err
}

// Owners holds the shared operations behind every search utility. One value
// serves many programs; per-invocation state lives in Prepared.
type Owners struct {
	Revisions native.ExecutionRevisions
	Pins      PinRecorder
	// Remover is required for find -delete; without it -delete refuses
	// before the walk starts.
	Remover Remover
	// ReadBytes bounds one captured input; zero selects the read door budget.
	ReadBytes int64
	// StreamBytes bounds one invocation's stdout; zero selects stream_bytes.
	StreamBytes int64
	// Now is find's reference time for -mtime; nil reads the clock once per
	// invocation.
	Now func() time.Time
	// Catalog shares validated directory membership and content filters.
	// Nil uses live traversal and content reads.
	Catalog *sourcecatalog.Catalog

	// overrides replaces catalog limits in fixtures that cannot reach them.
	overrides map[string]int64

	catalog toolcontract.LanguageCatalog
	specs   map[string]toolcontract.CommandSpecification
	ops     map[string]string
	regex   textsearch.Limits
}

type utility func(*invocation) error

var utilities = map[string]utility{"grep": runGrep, "rg": runRg, "find": runFind}

// NewOwners binds the frozen catalog's specifications and operation identities.
func NewOwners(revisions native.ExecutionRevisions, pins PinRecorder, remover Remover) (*Owners, error) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return nil, err
	}
	regex, err := textsearch.CatalogLimits()
	if err != nil {
		return nil, err
	}
	o := &Owners{Revisions: revisions, Pins: pins, Remover: remover, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, ops: map[string]string{}, regex: regex}
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
		if _, ok := o.specs[name]; !ok {
			return nil, fmt.Errorf("search utility %s is not in the language catalog", name)
		}
		if o.ops[name] == "" {
			return nil, fmt.Errorf("search utility %s has no catalog operation identity", name)
		}
	}
	return o, nil
}

// Operation returns the catalog operation identity that owns a command or
// one of its aliases.
func (o *Owners) Operation(command string) (string, bool) {
	spec, ok := o.spec(command)
	if !ok {
		return "", false
	}
	return o.ops[spec.Name], true
}

// Commands lists every catalog command these owners serve.
func (o *Owners) Commands() []string {
	out := make([]string, 0, len(o.specs))
	for _, command := range o.catalog.Commands {
		if _, ok := o.specs[command.Name]; ok {
			out = append(out, command.Name)
		}
	}
	return out
}

func (o *Owners) spec(word string) (toolcontract.CommandSpecification, bool) {
	if spec, ok := o.specs[word]; ok {
		return spec, true
	}
	for _, spec := range o.specs {
		for _, alias := range spec.Aliases {
			if alias == word {
				return spec, true
			}
		}
	}
	return toolcontract.CommandSpecification{}, false
}

// EffectMode names the catalog effect mode a bound form selects, for the
// dispatcher's per-operation admission: find -delete needs write and recovery,
// find -exec needs its bound child effect set, every other form reads.
func EffectMode(argv commandprogram.BoundArgv) string {
	if argv.Command != "find" {
		return "always"
	}
	if _, ok := argv.Last("-delete"); ok {
		return "-delete"
	}
	if _, ok := argv.Last("-exec"); ok {
		return "-exec"
	}
	return "default-predicates-print"
}

// Prepared is one bound utility invocation. InvokeOwner matches
// toolexecution.PreparedNativeEffect: streams reach it only after admission.
type Prepared struct {
	Operation string
	Argv      commandprogram.BoundArgv
	Review    commandprogram.ArgvReview
	owners    *Owners
	paths     *projectpaths.ProgramPaths
	dispatch  commandprogram.Dispatch
	request   commandprogram.OperationRequest
	spec      toolcontract.CommandSpecification
	run       utility
	bindErr   *commandprogram.ArgvError
}

// Prepare binds a reached command through the catalog option grammar. It
// performs no read. paths is the invocation's logical cwd; dispatch is the
// evaluator frame find -exec dispatches its child operations through (nil
// refuses -exec).
func (o *Owners) Prepare(paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	spec, ok := o.spec(request.Argv[0].Value)
	if !ok || paths == nil {
		return Prepared{}, fmt.Errorf("%s is not a search command", request.Argv[0].Value)
	}
	if request.OperationID != spec.Name {
		return Prepared{}, fmt.Errorf("request names operation %s, not command %s", request.OperationID, spec.Name)
	}
	bound, err := bindSearchArgv(spec, request.Argv)
	var bindErr *commandprogram.ArgvError
	if err != nil {
		if !errors.As(err, &bindErr) || bindErr.Code != "COMMAND_OPERAND_INVALID" {
			return Prepared{}, argvRejection(err, spec)
		}
	}
	return Prepared{Operation: o.ops[spec.Name], Argv: bound, Review: bound.Review(o.ops[spec.Name]), owners: o, paths: paths, dispatch: dispatch, request: request, spec: spec, run: utilities[spec.Name], bindErr: bindErr}, nil
}

// bindSearchArgv binds argv through CatalogBinder, preserving a signed numeric
// argument after BSD's "find ... -depth -N" as an operand rather than an
// unknown option flag.
func bindSearchArgv(spec toolcontract.CommandSpecification, argv commandprogram.Fields) (commandprogram.BoundArgv, error) {
	if spec.Name != "find" {
		return commandprogram.CatalogBinder{}.Bind(spec, argv)
	}
	var rewritten commandprogram.Fields
	var restore map[int]string
	for i := 1; i+1 < len(argv); i++ {
		if argv[i].Value == "--" {
			break
		}
		next := argv[i+1].Value
		if argv[i].Value == "-depth" && len(next) > 1 && next[0] == '-' && isASCIIDigits(next[1:]) {
			if rewritten == nil {
				rewritten = append(commandprogram.Fields(nil), argv...)
				restore = map[int]string{}
			}
			restore[i+1] = next
			rewritten[i+1].Value = "+" + next[1:]
		}
	}
	if rewritten == nil {
		return commandprogram.CatalogBinder{}.Bind(spec, argv)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, rewritten)
	for i := range bound.Items {
		if op := bound.Items[i].Operand; op != nil {
			for _, orig := range restore {
				if op.Value == "+"+orig[1:] {
					op.Value = orig
					break
				}
			}
		}
	}
	return bound, err
}

func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// InvokeOwner runs the bound utility once and settles its producer outcome.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if p.run == nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, fmt.Errorf("search invocation is not prepared")
	}
	inv := newInvocation(ctx, p, tc, streams)
	if p.spec.Name == "find" {
		inv.failure = 1
	}
	var err error
	if p.bindErr != nil {
		err = inv.usage(p.bindErr.Word, p.bindErr.Expected)
	} else {
		err = p.run(inv)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = inv.out.flush()
	}
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	return inv.settle(err)
}

// Invoke prepares and runs one request, settling a binding refusal as the
// producer outcome so callers without a separate review step see one result.
func (o *Owners) Invoke(ctx context.Context, tc tools.ToolContext, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	prepared, err := o.Prepare(paths, dispatch, request)
	if err != nil {
		result := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Terminal, result.StopCode = toolcontract.ProducerRefused, reject.Code
		}
		return result, err
	}
	return prepared.InvokeOwner(ctx, tc, streams)
}

func (o *Owners) readBytes() int64 {
	if o.ReadBytes > 0 {
		return o.ReadBytes
	}
	return readcaps.MaxFileBytes
}

func (o *Owners) streamBytes() int64 {
	if o.StreamBytes > 0 {
		return o.StreamBytes
	}
	return o.limit("stream_bytes")
}

func (o *Owners) limit(id string) int64 {
	if value, ok := o.overrides[id]; ok {
		return value
	}
	for _, limit := range o.catalog.Language.Limits {
		if limit.ID == id {
			return limit.Maximum
		}
	}
	return 0
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
