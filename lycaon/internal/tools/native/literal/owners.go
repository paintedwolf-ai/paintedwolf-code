// Package literal is the command frontend for literal content and metadata
// utilities. Each utility binds argv through the catalog and calls the same
// typed read, count, metadata and diff operations as the structured tools;
// stdout carries exact bytes while evidence stays in pins and mappings.
package literal

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
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

// Owners holds the shared typed operations behind every literal utility.
// One value serves many programs; per-invocation state lives in Prepared.
type Owners struct {
	Revisions native.ExecutionRevisions
	Pins      PinRecorder
	Domain    DomainOwners
	// ReadBytes bounds one captured input; zero selects the read door budget.
	ReadBytes int64
	// StreamBytes bounds one invocation's stdout; zero selects stream_bytes.
	StreamBytes int64

	// overrides replaces catalog limits in fixtures that cannot reach them.
	overrides map[string]int64

	accounts   *accounts
	volumes    volumeStats
	catalog    toolcontract.LanguageCatalog
	specs      map[string]toolcontract.CommandSpecification
	ops        map[string]string
	signatures signatures
}

type utility func(*invocation) error

var utilities = map[string]utility{
	"cat": runCat, "nl": runNl, "head": runHead, "tail": runTail, "wc": runWc,
	"ls": runLs, "stat": runStat, "diff": runDiff, "file": runFile,
	"xxd": runXxd, "hexdump": runHexdump, "od": runOd, "strings": runStrings, "base64": runBase64,
	"cmp": runCmp, "du": runDu, "df": runDf,
	"shasum": runChecksum, "sha1sum": runChecksum, "sha256sum": runChecksum, "sha512sum": runChecksum, "md5sum": runChecksum,
	"outline": runOutline, "summarize": runSummarize, "repo-map": runRepoMap,
}

// NewOwners binds the frozen catalog's specifications and operation identities.
func NewOwners(revisions native.ExecutionRevisions, pins PinRecorder, domain DomainOwners) (*Owners, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{Revisions: revisions, Pins: pins, Domain: domain, accounts: &accounts{}, volumes: fseffect.VolumeOf, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, ops: map[string]string{}}
	if o.signatures, err = decodeSignatures(catalog.Contracts["signatures"]); err != nil {
		return nil, err
	}
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
			return nil, fmt.Errorf("literal utility %s is not in the language catalog", name)
		}
		if o.ops[name] == "" {
			return nil, fmt.Errorf("literal utility %s has no catalog operation identity", name)
		}
	}
	return o, nil
}

// ForProgram binds memory for one program while retaining the shared account
// lookup cache and its lock alongside the immutable catalog and operations.
func (o *Owners) ForProgram(memory *toolcontract.ProgramMemory) *Owners {
	program := *o
	program.Revisions.Memory = memory
	return &program
}

// Operation returns the catalog operation identity that owns a command.
func (o *Owners) Operation(command string) (string, bool) {
	op, ok := o.ops[command]
	return op, ok
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

// Prepared is one bound utility invocation. InvokeOwner matches
// toolexecution.PreparedNativeEffect: streams reach it only after admission.
type Prepared struct {
	Operation string
	Argv      commandprogram.BoundArgv
	Review    commandprogram.ArgvReview
	owners    *Owners
	paths     *projectpaths.ProgramPaths
	request   commandprogram.OperationRequest
	spec      toolcontract.CommandSpecification
	run       utility
}

// Prepare binds a reached command through the catalog option grammar. It
// performs no read; paths is the invocation's logical cwd.
func (o *Owners) Prepare(paths *projectpaths.ProgramPaths, request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	command := request.OperationID
	spec, ok := o.specs[command]
	if !ok || paths == nil {
		return Prepared{}, fmt.Errorf("%s is not a literal content command", command)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
	if err != nil {
		return Prepared{}, argvRejection(err, spec)
	}
	review := bound.Review(o.ops[command])
	review.PathTargets = []commandprogram.PathTarget{}
	for _, operand := range bound.Operands() {
		if operand.Value != "-" {
			review.PathTargets = append(review.PathTargets, commandprogram.PathTarget{Path: operand.Value})
		}
	}
	for _, item := range bound.Items {
		if item.Option == nil || item.Option.ValueRole != "path" {
			continue
		}
		for _, value := range item.Option.Values {
			review.PathTargets = append(review.PathTargets, commandprogram.PathTarget{Path: value.Value})
		}
	}
	return Prepared{Operation: o.ops[command], Argv: bound, Review: review, owners: o, paths: paths, request: request, spec: spec, run: utilities[command]}, nil
}

// InvokeOwner runs the bound utility once and settles its producer outcome.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if p.run == nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, fmt.Errorf("literal invocation is not prepared")
	}
	if p.Argv.Help {
		if streams.Input != nil {
			_ = streams.Input.Close()
		}
		return commandprogram.WriteCatalogHelp(p.spec, streams.Output)
	}
	inv := newInvocation(ctx, p, tc, streams)
	defer inv.closeInputs()
	err := p.run(inv)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = inv.out.flush()
	}
	// The utility consumed what it needed; closing stdin is a consumer stop.
	if streams.Input != nil {
		_ = streams.Input.Close()
	}
	for _, closer := range inv.closers {
		_ = closer.Close()
	}
	return inv.settle(err)
}

func (o *Owners) readBytes() int64 {
	if o.ReadBytes > 0 {
		return o.ReadBytes
	}
	return o.limit("whole_file_bytes")
}

func (o *Owners) streamBytes() int64 {
	if o.StreamBytes > 0 {
		return o.StreamBytes
	}
	return o.limit("stream_throughput_bytes")
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
