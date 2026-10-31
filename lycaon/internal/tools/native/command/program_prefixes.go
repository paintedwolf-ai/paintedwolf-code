package command

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// prefixCommands run their COMMAND operand as a nested child through the
// same dispatch as a reached command: native names bind natively, other
// programs launch confined, and each child keeps its own receipt.
var prefixCommands = map[string]bool{"env": true, "timeout": true, "nohup": true}

// prefixedCommand is the inner argv a prefix command runs, without the
// prefix's own options and operands.
func prefixedCommand(name string, bound commandprogram.BoundArgv) commandprogram.Fields {
	operands := bound.Operands()
	switch name {
	case "timeout":
		if len(operands) > 1 {
			return operands[1:]
		}
	case "nohup":
		return operands
	}
	return nil
}

// usageStatus writes a prefix utility's diagnostic and status.
func usageStatus(streams commandprogram.Streams, status int, lines ...string) (toolcontract.ProducerResult, error) {
	for _, line := range lines {
		if _, err := fmt.Fprintln(nonNilWriter(streams.Error), line); err != nil {
			return ownerFailure(err), err
		}
	}
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func environmentMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok {
			out[name] = value
		}
	}
	return out
}

// timeoutBuiltin runs COMMAND under program supervision: on expiry its
// external process receives the signal (and KILL after -k), a native
// operation is interrupted, and the status is 124 unless --preserve-status.
// The command envelope's own deadline stays the outer bound.
func timeoutBuiltin(spec toolcontract.CommandSpecification, nested commandprogram.NestedCommand, clock commandprogram.Clock) commandprogram.OperationDispatcher {
	return jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(spec, err)
		}
		plan := commandprogram.TimeoutPlan{Signal: commandprogram.Signal{Name: "TERM", Number: 15}, PreserveStatus: len(bound.Options("--preserve-status")) > 0}
		if option, ok := bound.Last("-s"); ok {
			sig, valid := commandprogram.ParseSignal(option.Values[0].Value)
			if !valid {
				return usageStatus(streams, 125, fmt.Sprintf("timeout: %s: invalid signal", option.Values[0].Value), "Try 'timeout --help' for more information.")
			}
			plan.Signal = sig
		}
		if option, ok := bound.Last("-k"); ok {
			if plan.KillAfter, ok = commandprogram.ParseDuration(option.Values[0].Value); !ok {
				return usageStatus(streams, 125, fmt.Sprintf("timeout: invalid time interval '%s'", option.Values[0].Value), "Try 'timeout --help' for more information.")
			}
		}
		operands := bound.Operands()
		if len(operands) < 2 {
			return usageStatus(streams, 125, "timeout: missing operand", "Try 'timeout --help' for more information.")
		}
		var valid bool
		if plan.Duration, valid = commandprogram.ParseDuration(operands[0].Value); !valid {
			return usageStatus(streams, 125, fmt.Sprintf("timeout: invalid time interval '%s'", operands[0].Value), "Try 'timeout --help' for more information.")
		}
		inner := prefixedCommand(spec.Name, bound)
		var sent func(commandprogram.Signal)
		if len(bound.Options("-v")) > 0 {
			sent = func(sig commandprogram.Signal) {
				_, _ = fmt.Fprintf(nonNilWriter(streams.Error), "timeout: sending signal %s to command '%s'\n", sig.Name, inner[0].Value)
			}
		}
		if plan.Duration > 0 {
			restore := request.Wait.Deadline(clock.Now().Add(plan.Duration), strings.Join(request.Argv.Values(), " "))
			defer restore()
		}
		return commandprogram.RunTimeout(ctx, clock, plan, sent, func(ctx context.Context, relay *commandprogram.SignalRelay) (toolcontract.ProducerResult, error) {
			parent := request
			parent.Process.Signals = relay
			return nested.RunNested(ctx, parent, inner, environmentMap(request.Environment), streams)
		})
	})
}

// nohupBuiltin runs COMMAND with SIGHUP ignored. Program streams are not a
// terminal, so nothing goes to nohup.out; a redirect to nohup.out on the
// statement is refused before the command runs.
func nohupBuiltin(spec toolcontract.CommandSpecification, nested commandprogram.NestedCommand) commandprogram.OperationDispatcher {
	return jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(spec, err)
		}
		inner := prefixedCommand(spec.Name, bound)
		if len(inner) == 0 {
			return usageStatus(streams, 125, "nohup: missing operand", "Try 'nohup --help' for more information.")
		}
		if redirect, ok := nohupOutputRedirect(request.Statement); ok {
			reject := toolrejection.RejectInvalidArguments("COMMAND_OPTION_UNSUPPORTED", map[string]any{
				"command": spec.Name, "word": "nohup.out", "expected_form": spec.Argv,
				"source_start_byte": redirect.Span.StartByte, "source_end_byte": redirect.Span.EndByte, "source_line": redirect.Span.Line, "source_column": redirect.Span.Column,
			})
			return refusedResult("COMMAND_OPTION_UNSUPPORTED"), reject
		}
		parent := request
		parent.Process.IgnoreHangup = true
		return nested.RunNested(ctx, parent, inner, environmentMap(request.Environment), streams)
	})
}

// nohupOutputRedirect identifies literal terminal output file redirects.
func nohupOutputRedirect(statement commandsurface.Statement) (commandsurface.ProgramRedirect, bool) {
	for _, redirect := range statement.Redirects {
		if redirect.HereDocument != nil || strings.Contains(redirect.Operator, "<") {
			continue
		}
		var target strings.Builder
		literal := true
		for _, part := range redirect.Target.Parts {
			value, ok := part.(commandsurface.LiteralPart)
			if !ok {
				literal = false
				break
			}
			target.WriteString(value.Value)
		}
		if literal && path.Base(target.String()) == "nohup.out" {
			return redirect, true
		}
	}
	return commandsurface.ProgramRedirect{}, false
}
