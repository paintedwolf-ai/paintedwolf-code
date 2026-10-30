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
)

// sessionOutputResources is session output-lifetime accounting: retained
// state and codec memory beside a spill quota from the catalog.
type sessionOutputResources struct {
	*toolcontract.ProgramMemory
	mu           sync.Mutex
	spill, limit int64
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
	memory, _ := toolcontract.NewProgramMemory(limits)
	spill := int64(1 << 30)
	if catalog, err := toolcontract.LanguageV1(); err == nil {
		for _, limit := range catalog.Language.Limits {
			if limit.ID == "session_spill_bytes" {
				spill = limit.Maximum
			}
		}
	}
	return &sessionOutputResources{ProgramMemory: memory, limit: spill}
}

// jobBuiltin is a host builtin over the session's job registry.
type jobBuiltin func(context.Context, commandprogram.OperationRequest, commandprogram.Streams) (toolcontract.ProducerResult, error)

func (f jobBuiltin) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	return f(ctx, request, streams)
}

// jobBuiltins implement jobs, wait, kill and ps over session-owned handles
// only; no host-global PID or process name is ever addressed.
func jobBuiltins(jobs *SessionJobs, commands map[string]toolcontract.CommandSpecification) map[string]commandprogram.OperationDispatcher {
	list := jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		name := request.Argv[0].Value
		bound, err := (commandprogram.CatalogBinder{}).Bind(commands[name], request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(commands[name], err)
		}
		statuses, err := jobs.List(ctx)
		if err != nil {
			return ownerFailure(err), err
		}
		var targets []string
		if name == "ps" {
			for _, opt := range bound.Options("-p") {
				if len(opt.Values) > 0 {
					targets = append(targets, opt.Values[0].Value)
				}
			}
		}
		for _, op := range bound.Operands() {
			if name == "ps" && strings.TrimLeft(op.Value, "auxef") == "" {
				continue
			}
			targets = append(targets, op.Value)
		}
		if len(targets) > 0 {
			wanted := map[string]bool{}
			for _, target := range targets {
				handle, resolveErr := jobs.Resolve(target)
				if resolveErr != nil {
					_, _ = fmt.Fprintf(nonNilWriter(streams.Error), "%s: %s: session-owned background jobs only (no session job %s)\n", name, target, target)
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
		showCmd := name == "ps" || len(bound.Options("-l")) > 0
		var out strings.Builder
		for _, job := range statuses {
			if showCmd && job.CommandLine != "" {
				fmt.Fprintf(&out, "[%d] %s running %s\n", jobs.Number(job.Handle), job.Handle, job.CommandLine)
			} else {
				fmt.Fprintf(&out, "[%d] %s running\n", jobs.Number(job.Handle), job.Handle)
			}
		}
		if _, err := fmt.Fprint(nonNilWriter(streams.Output), out.String()); err != nil {
			return ownerFailure(err), err
		}
		return readResult(0), nil
	})
	return map[string]commandprogram.OperationDispatcher{
		"jobs": list,
		"ps":   list,
		"wait": jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			handles := request.Argv.Values()[1:]
			if len(handles) == 0 {
				var err error
				handles, err = jobs.Unwaited(ctx)
				if err != nil {
					return ownerFailure(err), err
				}
			}
			last := readResult(0)
			for _, handle := range handles {
				result, err := jobs.Wait(ctx, handle)
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

// argvRejection projects a binder refusal with its exact word span.
func argvRejection(spec toolcontract.CommandSpecification, cause error) error {
	code := "COMMAND_OPERAND_INVALID"
	data := map[string]any{"command": spec.Name, "expected_form": spec.Argv}
	var argvErr *commandprogram.ArgvError
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
	return toolrejection.RejectInvalidArguments(code, data)
}
