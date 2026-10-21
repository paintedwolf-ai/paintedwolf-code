package toolhost

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/literal"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/native/textutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// programOwnerTools are the structured tools the command program owners share.
type programOwnerTools struct {
	boundary   *sandbox.Boundary
	summarize  *survey.SummarizeTool
	repo       *reposurvey.RepoTool
	operations mutation.Operations
	catalog    *sourcecatalog.Catalog
}

// compose builds the literal and mutation owners once and returns the
// per-program factory: one publication barrier and redirect owner per program.
func (t programOwnerTools) compose(recorder *invocation.SQLRecorder) (func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error), error) {
	revisions := native.ExecutionRevisions{Boundary: t.boundary}
	literalOwners, err := literal.NewOwners(revisions, recorder, literal.DomainOwners{Summarize: t.summarize, Repo: t.repo})
	if err != nil {
		return nil, err
	}
	mutationOwners, err := mutation.NewOwners(revisions, recorder, t.operations)
	if err != nil {
		return nil, err
	}
	searchOwners, err := search.NewOwners(revisions, recorder, search.DeleteRemover{Delete: t.operations.Delete})
	if err != nil {
		return nil, err
	}
	searchOwners.Catalog = t.catalog
	textOwners, err := textutil.NewOwners(revisions, recorder)
	if err != nil {
		return nil, err
	}
	if textOwners.Regex, err = textsearch.NewPOSIXEngine(); err != nil {
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
			Builtins: func(ports command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
				spills, err := textutil.NewSessionSpills(ports.ToolContext, ports.ProgramID)
				if err != nil && !errors.Is(err, tools.ErrSessionScratchUnavailable) {
					return nil, nil, err
				}
				text := textutil.Ports{ToolContext: ports.ToolContext, Memory: ports.Memory, Admission: ports.Admission, Nested: ports.Nested, Publication: ports.Publication}
				release := func() {}
				if spills != nil {
					text.Spills = spills
					release = func() { _ = spills.Close() }
				}
				return textOwners.Builtins(text), release, nil
			},
			Redirections: func(frame *projectpaths.ProgramPaths) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
				publication := &mutation.Publication{Owners: mutationOwners, Program: mutation.Program{Paths: frame, Barrier: barrier}, Tools: tc}
				return publication, publication
			},
		}
		for _, name := range literalOwners.Commands() {
			tool, _ := literalOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := literalOwners.Prepare(frame, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		for _, name := range searchOwners.Commands() {
			tool, _ := searchOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := searchOwners.Prepare(frame, dispatch, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		for _, name := range mutationOwners.Commands() {
			tool, _ := mutationOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := mutationOwners.Prepare(mutation.Program{Paths: frame, Barrier: barrier}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		return owners, nil
	}, nil
}
