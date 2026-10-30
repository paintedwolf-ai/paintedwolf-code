package toolhost

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandspill"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/gitcmd"
	"github.com/lycaon/lycaon/internal/tools/native/hostenv"
	"github.com/lycaon/lycaon/internal/tools/native/literal"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/query"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/native/textutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// programOwnerTools are the structured tools the command program owners share.
type programOwnerTools struct {
	http       httpaction.Deps
	boundary   *sandbox.Boundary
	summarize  *survey.SummarizeTool
	repo       *reposurvey.RepoTool
	operations mutation.Operations
	catalog    *sourcecatalog.Catalog
	// git is the Git owner; nil leaves git to its unsupported refusal.
	git      *git.Manager
	registry *tools.DefaultRegistry
}

// compose builds the literal and mutation owners once and returns the
// per-program factory: one publication barrier and redirect owner per program.
func (t programOwnerTools) compose(services ProgramServices) (func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error), error) {
	recorder := services.Recorder
	revisions := native.ExecutionRevisions{Boundary: t.boundary}
	literalOwners, err := literal.NewOwners(revisions, recorder, literal.DomainOwners{Summarize: t.summarize, Repo: t.repo})
	if err != nil {
		return nil, err
	}
	mutationOwners, err := mutation.NewOwners(revisions, recorder, t.operations)
	if err != nil {
		return nil, err
	}
	queryOwners, err := query.NewOwners(revisions, recorder, t.operations.Write)
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
	hostenvOwners, err := hostenv.NewOwners(hostenv.OSHost{}, time.Now, t.boundary)
	if err != nil {
		return nil, err
	}
	gitNative, err := t.gitOperation()
	if err != nil {
		return nil, err
	}
	return func(tc tools.ToolContext, paths *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		barrier := &native.PublicationBarrier{}
		httpNative, err := t.httpOperation(services.HTTP, mutationOwners, barrier)
		if err != nil {
			return command.ProgramOwners{}, err
		}
		program := mutation.Program{Paths: paths, Barrier: barrier}
		owners := command.ProgramOwners{
			Natives:     map[string]command.NativeOperation{},
			Publication: &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc},
			Input:       &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc},
			Sources:     &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc},
			AdmitLaunch: barrier.AdmitDependentLaunch,
			Builtins: func(ports command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
				spills, err := commandspill.NewSessionSpills(ports.ToolContext.Host.SessionScratchDir, ports.ProgramID)
				if err != nil && !errors.Is(err, commandspill.ErrScratchUnavailable) {
					return nil, nil, err
				}
				text := textutil.Ports{ToolContext: ports.ToolContext, Memory: ports.Memory, Admission: ports.Admission, Nested: ports.Nested, Publication: ports.Publication}
				release := func() {}
				if spills != nil {
					text.Spills = spills
					release = func() { _ = spills.Close() }
				}
				builtins := textOwners.Builtins(text)
				var bound hostenv.NativeBindings
				if b, ok := ports.Nested.(hostenv.NativeBindings); ok {
					bound = b
				}
				for name, builtin := range hostenvOwners.Builtins(ports.ToolContext, ports.Nested, bound) {
					if _, taken := builtins[name]; !taken {
						builtins[name] = builtin
					}
				}
				return builtins, release, nil
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
				return command.NativePrepared{Mode: search.EffectMode(prepared.Argv), Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		for _, name := range mutationOwners.Commands() {
			tool, _ := mutationOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := mutationOwners.Prepare(mutation.Program{Paths: frame, Barrier: barrier}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		for _, name := range queryOwners.Commands() {
			tool, _ := queryOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := queryOwners.Prepare(query.Program{Paths: frame, Barrier: barrier}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		if httpNative.Prepare != nil {
			owners.Natives["curl"] = httpNative
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
	return command.NativeOperation{Tools: identities, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
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
