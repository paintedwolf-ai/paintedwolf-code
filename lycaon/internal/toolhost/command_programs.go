package toolhost

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/hostos"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
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
	// spills wraps each program's textutil spill storage when set.
	spills func(textutil.SpillStorage) textutil.SpillStorage
}

func directProgramOutputAdmission(current *tools.ToolContext) tools.CommandOutputAdmission {
	return func(_ context.Context, _ commandprogram.OutputDestination, _ string) (tools.CommandOutputWrite, error) {
		return tools.CommandOutputWrite{
			Publish: func(ctx context.Context, apply func(context.Context, tools.ToolContext) (commandprogram.OutputPublished, error)) (commandprogram.OutputPublished, error) {
				return apply(ctx, *current)
			},
			Abandon: func(context.Context, error) error { return nil },
		}, nil
	}
}

// compose builds the literal and mutation owners once and returns the
// per-program factory: one redirect owner per program.
func (t programOwnerTools) compose(recorder *invocation.SQLRecorder) (func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error), error) {
	revisions := native.ExecutionRevisions{Boundary: t.boundary}
	if t.catalog != nil {
		revisions.Literals = t.catalog.Literals
	}
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
	hostenvOwners, err := hostenv.NewOwners(hostos.Host{}, time.Now, t.boundary)

	if err != nil {
		return nil, err
	}
	gitNative, err := t.gitOperation()
	if err != nil {
		return nil, err
	}
	return func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		literalProgram := literalOwners.ForProgram(memory)
		searchProgram, queryProgram, mutationProgram, textProgram := *searchOwners, *queryOwners, *mutationOwners, *textOwners
		searchProgram.Revisions.Memory = memory
		searchProgram.Admission = admission
		queryProgram.Revisions.Memory = memory
		mutationProgram.Revisions.Memory = memory
		mutationProgram.Admission = admission
		textProgram.Revisions.Memory = memory
		literalOwners, searchOwners, queryOwners, mutationOwners, textOwners := literalProgram, &searchProgram, &queryProgram, &mutationProgram, &textProgram
		program := mutation.Program{Paths: paths}
		httpNative, err := t.httpOperation(mutationOwners)
		if err != nil {
			return command.ProgramOwners{}, err
		}
		baseSpills := &textutil.Spills{}
		var spills textutil.SpillStorage = baseSpills
		if t.spills != nil {
			spills = t.spills(spills)
		}
		owners := command.ProgramOwners{
			PublishDocument: mutationOwners.Operations.Write.PublishAccepted,
			Natives:         map[string]command.NativeOperation{},
			Publication: func(admit tools.CommandOutputAdmission) commandprogram.OutputPublication {
				return &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc, Admission: admit}
			},
			Input:   &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc},
			Sources: &mutation.Publication{Owners: mutationOwners, Program: program, Tools: tc},
			Builtins: func(ports command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
				builtinTC := ports.ToolContext
				builtinPub := &mutation.Publication{Owners: mutationOwners, Program: program, Tools: builtinTC, Admission: directProgramOutputAdmission(&builtinTC)}
				text := textutil.Ports{ToolContext: ports.ToolContext, Memory: ports.Memory, Admission: ports.Admission, Nested: ports.Nested, Publication: builtinPub, Spills: spills}
				release := func() { _ = baseSpills.Close() }
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
			Redirections: func(frame *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
				publication := &mutation.Publication{Owners: mutationOwners, Program: mutation.Program{Paths: frame}, Tools: tc, Admission: admit}
				return publication, publication
			},
		}
		for _, name := range textOwners.Commands() {
			if !textutil.PathUtility(name) {
				continue
			}
			owners.Natives[name] = command.NativeOperation{Tool: name, ClosedForms: name == "sed", Prepare: func(ctx context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				request.Invocation.Scope.Directory = frame.Cwd().DisplayPath
				currentTC := frame.ToolContext()
				publication := &mutation.Publication{Owners: mutationOwners, Program: mutation.Program{Paths: frame}, Tools: currentTC, Admission: directProgramOutputAdmission(&currentTC)}
				ports := textutil.Ports{ToolContext: currentTC, Memory: memory, Admission: admission, Publication: publication, Spills: spills}
				prepared, err := textOwners.PrepareNative(ctx, frame, ports, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{
					Mode: prepared.Mode, Review: prepared.Review, Release: prepared.Close, DeferredSource: prepared.DeferredSource,
					RecordSourceRead: prepared.RecordSourceRead,
					Invoke: func(ctx context.Context, admitted tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
						currentTC = admitted
						publication.Tools = admitted
						ports.ToolContext = admitted
						return prepared.InvokeOwner(ctx, ports, streams)
					},
				}, nil
			}}
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
				prepared, err := mutationOwners.Prepare(mutation.Program{Paths: frame}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		for _, name := range queryOwners.Commands() {
			tool, _ := queryOwners.Operation(name)
			owners.Natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := queryOwners.Prepare(query.Program{Paths: frame}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		if httpNative.Prepare != nil {
			owners.Natives["curl"] = httpNative
		}
		if gitNative != nil {
			owners.Natives["git"] = gitNative(mutationOwners)
		}
		return owners, nil
	}, nil
}

// gitOperation serves the git umbrella command: each bound form keeps its
// structured Git tool's identity where one is registered, and otherwise runs
// as the catalog command's own read execution. Each program binds it to its
// own staged publication, which git apply shares with patch.
func (t programOwnerTools) gitOperation() (func(*mutation.Owners) command.NativeOperation, error) {
	if t.git == nil {
		return nil, nil
	}
	owners, err := gitcmd.NewOwners(t.git)
	if err != nil {
		return nil, err
	}
	owners.Boundary = t.boundary
	registered := func(tool string) bool {
		if tool == "" || t.registry == nil {
			return false
		}
		_, ok := t.registry.Operation(tool)
		return ok
	}
	var identities []string
	for _, name := range owners.Commands() {
		if tool := owners.Operation(name); registered(tool) {
			identities = append(identities, tool)
		}
	}
	return func(filesystem *mutation.Owners) command.NativeOperation {
		return command.NativeOperation{Tools: identities, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			nested, _ := dispatch.(commandprogram.NestedCommand)
			prepared, err := owners.PrepareGit(frame, filesystem, nested, request)
			if err != nil {
				return command.NativePrepared{}, err
			}
			tool := prepared.Operation
			if !registered(tool) {
				tool = ""
			}
			return command.NativePrepared{Command: prepared.Command, Tool: tool, Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
		}}
	}, nil
}
