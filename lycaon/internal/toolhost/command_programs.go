package toolhost

import (
	"context"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/sandbox"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
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
		return owners, nil
	}, nil
}
