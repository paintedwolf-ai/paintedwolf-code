package toolhost

import (
	"context"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/sandbox"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/gitcmd"
	"github.com/lycaon/lycaon/internal/tools/native/literal"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// programOwnerTools are the structured tools the command program owners share.
type programOwnerTools struct {
	boundary   *sandbox.Boundary
	summarize  *survey.SummarizeTool
	repo       *reposurvey.RepoTool
	operations mutation.Operations
	// git is the Git owner; nil leaves git to its unsupported refusal.
	git      *git.Manager
	registry *tools.DefaultRegistry
}

type pinRecorderAdapter struct{ *invocation.SQLRecorder }

func (a pinRecorderAdapter) RecordRevisionPins(ctx context.Context, executionID string, pins []toolcontract.ExecutionRevision) error {
	return a.SQLRecorder.RecordRevisionPins(ctx, executionID, 0, pins)
}

// compose builds the literal and mutation owners once and returns the
// per-program factory: one publication barrier and redirect owner per program.
func (t programOwnerTools) compose(recorder *invocation.SQLRecorder) (func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error), error) {
	revisions := native.ExecutionRevisions{Boundary: t.boundary}
	var pins pinRecorderAdapter
	if recorder != nil {
		pins = pinRecorderAdapter{recorder}
	}
	literalOwners, err := literal.NewOwners(revisions, pins, literal.DomainOwners{Summarize: t.summarize, Repo: t.repo})
	if err != nil {
		return nil, err
	}
	mutationOwners, err := mutation.NewOwners(revisions, pins, t.operations)
	if err != nil {
		return nil, err
	}
	gitNative, err := t.gitOperation()
	if err != nil {
		return nil, err
	}
	return func(tc tools.ToolContext, paths *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		barrier := &native.PublicationBarrier{}
		program := mutation.Program{Paths: paths, Barrier: barrier}
		owners := command.ProgramOwners{
			Natives:     map[string]command.NativeOperation{},
			Publication: &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc},
			Input:       &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc},
			AdmitLaunch: barrier.AdmitDependentLaunch,
		}
		for _, name := range literalOwners.Commands() {
			tool, _ := literalOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := literalOwners.Prepare(frame, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		for _, name := range mutationOwners.Commands() {
			tool, _ := mutationOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := mutationOwners.Prepare(mutation.Program{Paths: frame, Barrier: barrier}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		if gitNative.Prepare != nil {
			owners.Natives["git"] = gitNative
		}
		return owners, nil
	}, nil
}

// gitOperation serves the git umbrella command: each bound form keeps its
// structured Git tool's identity where one is registered, and otherwise runs
// as the catalog command's own read execution.
func (t programOwnerTools) gitOperation() (command.NativeOperation, error) {
	if t.git == nil {
		return command.NativeOperation{}, nil
	}
	owners, err := gitcmd.NewOwners(t.git)
	if err != nil {
		return command.NativeOperation{}, err
	}
	owners.Boundary = t.boundary
	registered := func(tool string) bool {
		if tool == "" || t.registry == nil {
			return false
		}
		_, ok := t.registry.Definition(tool)
		return ok
	}
	var identities []string
	for _, name := range owners.Commands() {
		if tool := owners.Operation(name); registered(tool) {
			identities = append(identities, tool)
		}
	}
	return command.NativeOperation{Tools: identities, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, request commandprogram.OperationRequest) (command.NativePrepared, error) {
		prepared, err := owners.PrepareGit(frame, request)
		if err != nil {
			return command.NativePrepared{}, err
		}
		tool := prepared.Operation
		if !registered(tool) {
			tool = ""
		}
		return command.NativePrepared{Command: prepared.Command, Tool: tool, Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
	}}, nil
}
