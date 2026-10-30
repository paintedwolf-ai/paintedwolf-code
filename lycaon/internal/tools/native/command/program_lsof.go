package command

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// SessionListeners lists listening sockets held by one session's lineages.
type SessionListeners func(context.Context, string) ([]lineage.SocketObservation, error)

// lsofBuiltin answers `lsof [-i :PORT|-iTCP:PORT] [-sTCP:LISTEN]` from the
// session's own observed listeners. A requested port or grant alone shows
// nothing; status 1 means no matching owned listener.
func lsofBuiltin(spec toolcontract.CommandSpecification, sessionID string, listeners SessionListeners) jobBuiltin {
	return func(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
		if err != nil {
			return refusedResult("COMMAND_OPERAND_INVALID"), argvRejection(spec, err)
		}
		if len(bound.Operands()) != 0 {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "lsof", "reason": "takes no operands"})
		}
		port := 0
		if option, ok := bound.Last("-i"); ok {
			if port, err = lsofPort(option.Values[0].Value); err != nil {
				return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "lsof", "word": option.Values[0].Value, "expected": ":PORT or TCP:PORT"})
			}
		}
		if option, ok := bound.Last("-s"); ok && option.Values[0].Value != "TCP:LISTEN" {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "lsof", "word": option.Values[0].Value, "expected": "TCP:LISTEN"})
		}
		observed, err := listeners(ctx, sessionID)
		if err != nil {
			return ownerFailure(err), err
		}
		var out strings.Builder
		for _, socket := range observed {
			if port != 0 && int(socket.Local.Port()) != port {
				continue
			}
			fmt.Fprintf(&out, "%d %s LISTEN\n", socket.Local.Port(), socket.Local)
		}
		if out.Len() == 0 {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
		}
		if _, err := fmt.Fprint(nonNilWriter(streams.Output), out.String()); err != nil {
			return ownerFailure(err), err
		}
		return readResult(0), nil
	}
}

func lsofPort(endpoint string) (int, error) {
	value, ok := strings.CutPrefix(endpoint, "TCP:")
	if !ok {
		value, ok = strings.CutPrefix(endpoint, ":")
	}
	port, err := strconv.Atoi(value)
	if !ok || err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid lsof endpoint %q", endpoint)
	}
	return port, nil
}
