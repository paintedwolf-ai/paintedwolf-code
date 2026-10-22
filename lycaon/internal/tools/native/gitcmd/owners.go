// Package gitcmd is the command frontend for Git. Read commands run the
// bundled Git through the Git owner's hermetic read door and stream Git's own
// output and status; their argv is the catalog-bound command, the repository
// must lie inside the program's roots, and forms that would write (branch
// NAME, config KEY VALUE, remote add) are refused before Git runs.
package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Reader is the Git owner's read door (git.CommandReader.ReadCommand).
type Reader interface {
	ReadCommand(ctx context.Context, dir string, args, config []string, stdout io.Writer) ([]byte, int, error)
}

// readForms are the catalog read commands and their operand rules.
var readForms = map[string]operandRule{
	"git status": anyOperands, "git diff": anyOperands, "git log": anyOperands, "git show": anyOperands,
	"git blame": blameOperands, "git branch": branchOperands, "git tag": tagOperands, "git rev-parse": anyOperands,
	"git rev-list": revListOperands, "git merge-base": mergeBaseOperands, "git remote": remoteOperands,
	"git config": configOperands, "git stash list": noOperands, "git worktree list": noOperands, "git ls-files": anyOperands,
}

// Owners holds the Git read frontends.
type Owners struct {
	Git Reader

	catalog toolcontract.LanguageCatalog
	specs   map[string]toolcontract.CommandSpecification
	ops     map[string]string
	// limitOverride narrows catalog limits for bounded fixtures.
	limitOverride map[string]int64
}

// NewOwners binds the frozen catalog's Git read specifications. A command
// whose structured tool row exists keeps that operation identity; the rest
// have none and run as catalog-attributed host commands.
func NewOwners(reader Reader) (*Owners, error) {
	if reader == nil {
		return nil, errors.New("git command owners require the Git read door")
	}
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{Git: reader, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, ops: map[string]string{}}
	for _, command := range catalog.Commands {
		if _, ok := readForms[command.Name]; ok {
			o.specs[command.Name] = command
		}
	}
	for name := range readForms {
		if _, ok := o.specs[name]; !ok {
			return nil, fmt.Errorf("git read command %s has no catalog specification", name)
		}
	}
	for _, disposition := range catalog.Tools {
		for _, form := range disposition.CommandForms {
			if _, ok := o.specs[form]; ok {
				o.ops[form] = disposition.Tool
			}
		}
	}
	return o, nil
}

// Commands lists the served commands in catalog order.
func (o *Owners) Commands() []string {
	var out []string
	for _, command := range o.catalog.Commands {
		if _, ok := o.specs[command.Name]; ok {
			out = append(out, command.Name)
		}
	}
	return out
}

// Operation returns the structured tool identity a command shares, or "".
func (o *Owners) Operation(command string) string { return o.ops[command] }

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

// Prepared is one bound Git command, ready for admission.
type Prepared struct {
	// Command is the catalog command the argv bound to.
	Command   string
	Operation string
	Argv      commandprogram.BoundArgv
	Review    commandprogram.ArgvReview
	command   *command
	owners    *Owners
	paths     *projectpaths.ProgramPaths
}

// Prepare binds a reached Git command through the catalog grammar and its
// operand rule. It reads and runs nothing.
func (o *Owners) Prepare(paths *projectpaths.ProgramPaths, request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	spec, ok := o.specs[request.OperationID]
	if !ok || paths == nil {
		return Prepared{}, fmt.Errorf("%s is not a Git read command", request.OperationID)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
	if err != nil {
		return Prepared{}, argvRejection(err, spec)
	}
	parsed, err := parseCommand(spec, request.Argv, bound)
	if err != nil {
		return Prepared{}, err
	}
	reviewID := o.ops[spec.Name]
	if reviewID == "" {
		reviewID = spec.Name
	}
	return Prepared{Command: spec.Name, Operation: o.ops[spec.Name], Argv: bound, Review: bound.Review(reviewID), command: parsed, owners: o, paths: paths}, nil
}

// InvokeOwner runs the bound command once and settles Git's status.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if p.command == nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, errors.New("git command is not prepared")
	}
	if streams.Input != nil {
		defer func() { _ = streams.Input.Close() }()
	}
	status, err := p.run(ctx, streams)
	if err == nil {
		err = ctx.Err()
	}
	return settle(status, err, p.command.spec.Name)
}

// settle projects Git's exit onto the producer contract. Read commands have
// no effect; diff --exit-code and merge-base --is-ancestor report status 1
// as an answer, which still publishes.
func settle(status int, err error, name string) (toolcontract.ProducerResult, error) {
	if err != nil {
		result := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Terminal, result.StopCode = toolcontract.ProducerRefused, reject.Code
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Status, result.Terminal, result.StopCode = 130, toolcontract.ProducerInterrupted, ""
		}
		return result, err
	}
	result := toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
	result.PublicationEligible = status == 0
	if status == 1 && (name == "git diff" || name == "git merge-base") {
		result.PublicationEligible, result.PublicationBasis = true, toolcontract.NativeComparisonPublication
	}
	return result, nil
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
