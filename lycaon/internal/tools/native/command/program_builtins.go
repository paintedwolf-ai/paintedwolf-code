package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// sessionOutputResources is session output-lifetime accounting: retained
// state and codec memory beside a spill quota from the catalog.
type sessionOutputResources struct {
	*toolcontract.ProgramMemory
	mu           sync.Mutex
	spill, limit int64
}

func (s *sessionOutputResources) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	release, err := s.ProgramMemory.ReserveProgramState(ctx, n)
	var limit *toolcontract.ProgramMemoryLimitError
	if errors.As(err, &limit) {
		return nil, &toolcontract.ProgramMemoryLimitError{Account: "session_output_state", Maximum: limit.Maximum, Observed: limit.Observed}
	}
	return release, err
}

func (s *sessionOutputResources) ReserveSpill(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 0 || n > s.limit-s.spill {
		return nil, &toolcontract.ProgramMemoryLimitError{Account: "session_spill_bytes", Maximum: s.limit, Observed: s.spill + n}
	}
	s.spill += n
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.spill -= n
			s.mu.Unlock()
		})
	}, nil
}

// sessionResources is one session's output-lifetime account; sessions never
// share a quota, so one session's retained output cannot starve another.
func (r *ProgramRuntime) sessionResources(sessionID string) *sessionOutputResources {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionResourcesLocked(sessionID)
}

func (r *ProgramRuntime) sessionResourcesLocked(sessionID string) *sessionOutputResources {
	if resources, ok := r.resources[sessionID]; ok {
		return resources
	}
	if r.resources == nil {
		r.resources = map[string]*sessionOutputResources{}
	}
	resources := newSessionOutputResources()
	r.resources[sessionID] = resources
	return resources
}

func newSessionOutputResources() *sessionOutputResources {
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		limits = toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 64 << 20}
	}
	// A spill read's decoder scratch, its screening window and one program's
	// working storage can be held at once by the session's output lifetime.
	limits.Working *= 3
	// Durable output handles outlive one program. Their own session account can
	// retain a full child-execution budget and a later command without charging
	// the settled program's 64 MiB state account.
	limits.State = 256 << 20
	spill := int64(1 << 30)
	if catalog, err := toolcontract.ReadOnlyLanguageV1(); err == nil {
		for _, limit := range catalog.Language.Limits {
			switch limit.ID {
			case "session_spill_bytes":
				spill = limit.Maximum
			case "session_output_state_bytes":
				limits.State = limit.Maximum
			}
		}
	}
	memory, _ := toolcontract.NewProgramMemory(limits)
	return &sessionOutputResources{ProgramMemory: memory, limit: spill}
}

// jobBuiltin is a host builtin over the session's job registry.
type jobBuiltin func(context.Context, commandprogram.OperationRequest, commandprogram.Streams) (toolcontract.ProducerResult, error)

func (f jobBuiltin) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	return f(ctx, request, streams)
}

// jobBuiltins implement jobs, wait, kill, ps, pgrep and pkill. Read-only
// process inspection (ps, pgrep) lists visible host processes and marks
// session-owned jobs; signalling (kill, pkill) is restricted to session-owned
// jobs and refuses foreign processes with PROCESS_NOT_OWNED.
func jobBuiltins(jobs *SessionJobs, commands map[string]toolcontract.CommandSpecification) map[string]commandprogram.OperationDispatcher {
	listJobs := jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := (commandprogram.CatalogBinder{}).Bind(commands["jobs"], request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(commands["jobs"], err)
		}
		statuses, err := jobs.List(ctx)
		if err != nil {
			return ownerFailure(err), err
		}
		var targets []string
		for _, op := range bound.Operands() {
			targets = append(targets, op.Value)
		}
		if len(targets) > 0 {
			wanted := map[string]bool{}
			for _, target := range targets {
				handle, resolveErr := jobs.Resolve(target)
				if resolveErr != nil {
					_, _ = fmt.Fprintf(nonNilWriter(streams.Error), "jobs: %s: session-owned background jobs only (no session job %s)\n", target, target)
					continue
				}
				wanted[handle] = true
			}
			filtered := statuses[:0]
			for _, job := range statuses {
				if wanted[job.Handle] {
					filtered = append(filtered, job)
				}
			}
			statuses = filtered
		}
		showCmd := len(bound.Options("-l")) > 0
		var out strings.Builder
		for _, job := range statuses {
			if showCmd && job.CommandLine != "" {
				fmt.Fprintf(&out, "[%d] %d running %s\n", jobs.Number(job.Handle), jobs.publicID(job.Handle), job.CommandLine)
			} else {
				fmt.Fprintf(&out, "[%d] %d running\n", jobs.Number(job.Handle), jobs.publicID(job.Handle))
			}
		}
		if _, err := fmt.Fprint(nonNilWriter(streams.Output), out.String()); err != nil {
			return readFailure(err), err
		}
		return readResult(0), nil
	})
	return map[string]commandprogram.OperationDispatcher{
		"jobs":  listJobs,
		"ps":    psBuiltin(jobs, commands["ps"]),
		"pgrep": pgrepBuiltin(jobs, commands["pgrep"]),
		"pkill": pkillBuiltin(jobs, commands["pkill"]),
		"wait": jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			handles := request.Argv.Values()[1:]
			if len(handles) == 0 {
				var err error
				handles, err = jobs.Unwaited(ctx)
				if err != nil {
					return ownerFailure(err), err
				}
			}
			exact := make([]string, len(handles))
			for i, reference := range handles {
				handle, err := jobs.Resolve(reference)
				if err != nil {
					return ownerFailure(err), err
				}
				exact[i] = handle
			}
			last := readResult(0)
			for _, handle := range exact {
				result, err := jobs.Wait(ctx, handle, request.Wait, strings.Join(request.Argv.Values(), " "))
				if err != nil {
					if reject := toolrejection.AsToolReject(err); reject != nil && reject.Data != nil {
						reject.Data["command"] = "wait"
					}
					return ownerFailure(err), err
				}
				last = toolcontract.ProducerResult{Status: result.Status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: result.Status == 0, EffectCertainty: toolcontract.NoEffect}
			}
			return last, nil
		}),
		"kill": jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			bound, err := commandprogram.CatalogBinder{}.Bind(commands["kill"], request.Argv)
			if err != nil {
				return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(commands["kill"], err)
			}
			signal := "TERM"
			if option, ok := bound.Last("-s"); ok {
				signal = option.Values[0].Value
			}
			argv := bound.Operands().Values()
			if len(argv) == 0 {
				return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "kill", "reason": "requires a session job"})
			}
			for _, handle := range argv {
				if err := jobs.Signal(ctx, handle, signal); err != nil {
					if reject := toolrejection.AsToolReject(err); reject != nil && reject.Data != nil {
						reject.Data["command"] = "kill"
					}
					return ownerFailure(err), err
				}
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectAttempted}, nil
		}),
	}
}

func readResult(status int) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}
}

// readFailure is a read-only builtin whose output could not be written: it
// changed nothing, so a reader that finished first settles it as a closed pipe.
func readFailure(err error) toolcontract.ProducerResult {
	result := ownerFailure(err)
	result.EffectCertainty = toolcontract.NoEffect
	return result
}

// argvRejection projects a binder refusal with its exact word span. A typed
// host refusal an owner raised while binding (a path outside every folder, a
// folder no longer attached) keeps its own code and facts.
func argvRejection(spec toolcontract.CommandSpecification, cause error) error {
	cause = tools.CommandResourceRejection(cause)
	if owner := toolrejection.AsToolReject(cause); owner != nil && programResourceRefusal(owner) {
		if owner.Data == nil {
			owner.Data = map[string]any{}
		}
		if _, ok := owner.Data["command"]; !ok {
			owner.Data["command"] = spec.Name
		}
		return cause
	}
	code := "COMMAND_OPERAND_INVALID"
	data := map[string]any{"command": spec.Name, "expected_form": spec.Argv}
	var argvErr *commandprogram.ArgvError
	if reject := toolrejection.AsToolReject(cause); reject != nil && !errors.As(cause, &argvErr) {
		return reject
	}
	if errors.As(cause, &argvErr) {
		code = argvErr.Code
		data["word"], data["expected"] = argvErr.Word, argvErr.Expected
		data["source_start_byte"], data["source_end_byte"], data["source_line"], data["source_column"] = argvErr.Span.StartByte, argvErr.Span.EndByte, argvErr.Span.Line, argvErr.Span.Column
	} else if owner := toolrejection.AsToolReject(cause); owner != nil {
		// An owner's own operand rule keeps its code and facts.
		code = owner.Code
		for key, value := range owner.Data {
			data[key] = value
		}
	}
	return errors.Join(toolrejection.RejectInvalidArguments(code, data), cause)
}
