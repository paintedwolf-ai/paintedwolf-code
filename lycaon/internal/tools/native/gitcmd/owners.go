// Package gitcmd is the command frontend for Git. Read commands run the
// bundled Git through the Git owner's hermetic read door and stream Git's own
// output and status; mutation and network commands translate catalog-bound
// argv into shared Git owner requests with host review, confinement, and
// remote host gating.
package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Backend is the Git owner's read, mutation, worktree and network surface.
type Backend interface {
	git.GitManager
	InitCommand(ctx context.Context, dir, target string, config, options []string, stdout io.Writer) ([]byte, int, error)
	NetworkCommand(ctx context.Context, dir string, args []string, stdout io.Writer) ([]byte, int, error)
	RemoteURL(ctx context.Context, dir string, args []string) string
	AddWorktree(ctx context.Context, toplevel, path, branch, baseRef string) error
	RemoveWorktree(ctx context.Context, toplevel, path string) error
	PruneWorktrees(ctx context.Context, toplevel string) error
}

// readForms are the catalog read commands and their operand rules.
var readForms = map[string]operandRule{
	"git status": anyOperands, "git diff": anyOperands, "git log": anyOperands, "git show": anyOperands,
	"git blame": blameOperands, "git branch": branchOperands, "git tag": tagOperands, "git rev-parse": anyOperands,
	"git rev-list": revListOperands, "git merge-base": mergeBaseOperands, "git remote": remoteOperands,
	"git config": configOperands, "git stash list": noOperands, "git worktree list": noOperands, "git ls-files": anyOperands,
}

// Owners holds the Git read, mutation and network frontends.
type Owners struct {
	Git      Backend
	Boundary *sandbox.Boundary

	catalog toolcontract.LanguageCatalog
	specs   map[string]toolcontract.CommandSpecification
	ops     map[string]string
	// limitOverride narrows catalog limits for bounded fixtures.
	limitOverride map[string]int64

	mu     sync.Mutex
	staged map[string]map[string]bool
}

// NewOwners binds the frozen catalog's Git command specifications. A command
// whose structured tool row exists keeps that operation identity; the rest
// have none and run as catalog-attributed host commands.
func NewOwners(backend Backend) (*Owners, error) {
	if backend == nil {
		return nil, errors.New("git command owners require the Git backend")
	}
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{
		Git:     backend,
		catalog: catalog,
		specs:   map[string]toolcontract.CommandSpecification{},
		ops:     map[string]string{},
		staged:  map[string]map[string]bool{},
	}
	for _, command := range catalog.Commands {
		if _, ok := readForms[command.Name]; ok {
			o.specs[command.Name] = command
		}
		if _, ok := mutateForms[command.Name]; ok {
			o.specs[command.Name] = command
		}
	}
	for name := range readForms {
		if _, ok := o.specs[name]; !ok {
			return nil, fmt.Errorf("git read command %s has no catalog specification", name)
		}
	}
	for name := range mutateForms {
		if _, ok := o.specs[name]; !ok {
			return nil, fmt.Errorf("git mutation command %s has no catalog specification", name)
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
	Mode      string
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
		return Prepared{}, fmt.Errorf("%s is not a Git command", request.OperationID)
	}
	if err := validateGitEnvironment(spec, request.Environment); err != nil {
		return Prepared{}, err
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
	return Prepared{
		Command:   spec.Name,
		Operation: o.ops[spec.Name],
		Mode:      effectMode(spec, parsed, bound),
		Argv:      bound,
		Review:    bound.Review(reviewID),
		command:   parsed,
		owners:    o,
		paths:     paths,
	}, nil
}

// InvokeOwner runs the bound command once and settles Git's status.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if p.command == nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, errors.New("git command is not prepared")
	}
	if streams.Input != nil {
		defer func() { _ = streams.Input.Close() }()
	}
	if _, ok := mutateForms[p.command.spec.Name]; ok {
		return invokeMutation(ctx, p, tc, streams)
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

var blockedGitEnvKeys = map[string]bool{
	"SSH_AUTH_SOCK":       true,
	"SSH_ASKPASS":         true,
	"SSH_ASKPASS_REQUIRE": true,
}

func validateGitEnvironment(spec toolcontract.CommandSpecification, entries []string) error {
	for _, entry := range entries {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(name)
		if blockedGitEnvKeys[strings.ToUpper(trimmed)] {
			return operandInvalid(spec, entry, "an environment without SSH transport override variables")
		}
		if _, err := exec.AgentLaunchOptions(exec.LaunchPlan{}, "", []string{entry}, nil, false); err != nil {
			return operandInvalid(spec, entry, "an environment without host-reserved variables")
		}
	}
	return nil
}
