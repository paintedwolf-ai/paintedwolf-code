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

func (r *ProgramRuntime) sessionResources() *sessionOutputResources {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resources != nil {
		return r.resources
	}
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		limits = toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 64 << 20}
	}
	memory, _ := toolcontract.NewProgramMemory(limits)
	spill := int64(1 << 30)
	if catalog, err := toolcontract.LanguageV1(); err == nil {
		for _, limit := range catalog.Language.Limits {
			if limit.ID == "session_spill_bytes" {
				spill = limit.Maximum
			}
		}
	}
	r.resources = &sessionOutputResources{ProgramMemory: memory, limit: spill}
	return r.resources
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
		if _, err := (commandprogram.CatalogBinder{}).Bind(commands[request.Argv[0].Value], request.Argv); err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(commands[request.Argv[0].Value], err)
		}
		if len(request.Argv) != 1 {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": request.Argv[0].Value, "reason": "takes no operands"})
		}
		statuses, err := jobs.List(ctx)
		if err != nil {
			return ownerFailure(err), err
		}
		var out strings.Builder
		for _, job := range statuses {
			out.WriteString(job.Handle + " running -\n")
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
				statuses, err := jobs.List(ctx)
				if err != nil {
					return ownerFailure(err), err
				}
				for _, job := range statuses {
					handles = append(handles, job.Handle)
				}
			}
			last := readResult(0)
			for _, handle := range handles {
				result, err := jobs.Wait(ctx, handle)
				if err != nil {
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
	data := map[string]any{"command": spec.Name}
	var argvErr *commandprogram.ArgvError
	if errors.As(cause, &argvErr) {
		code = argvErr.Code
		data["word"], data["expected"] = argvErr.Word, argvErr.Expected
		data["source_start_byte"], data["source_end_byte"], data["source_line"], data["source_column"] = argvErr.Span.StartByte, argvErr.Span.EndByte, argvErr.Span.Line, argvErr.Span.Column
	}
	return toolrejection.RejectInvalidArguments(code, data)
}
