package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// FrameCommand runs export or unset, the catalog commands that change the
// invocation frame the evaluator owns. Argv binds through the catalog option
// grammar; operands are data: NAME or NAME=VALUE for export, NAME for unset.
// Every operand is validated before the frame changes, so a refused command
// leaves the frame as it was.
func FrameCommand(ctx context.Context, frame *Variables, spec toolcontract.CommandSpecification, argv Fields) (toolcontract.ProducerResult, error) {
	if err := ctx.Err(); err != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, err
	}
	refused := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, EffectCertainty: toolcontract.NoEffect}
	if frame == nil || (spec.Name != "export" && spec.Name != "unset") {
		return refused, fmt.Errorf("%q is not a frame command", spec.Name)
	}
	bound, err := CatalogBinder{}.Bind(spec, argv)
	if err != nil {
		refused.StopCode = argvCode(err)
		return refused, err
	}
	operands := bound.Operands()
	for _, operand := range operands {
		name, value, assigned := strings.Cut(operand.Value, "=")
		if assigned && spec.Name == "unset" {
			name = operand.Value
		}
		err := assignable(frame.bounds, name, value)
		var refusal *ExpansionError
		if errors.As(err, &refusal) {
			refusal.Span = operand.Span
			refused.StopCode = refusal.Code
			return refused, err
		}
	}
	for _, operand := range operands {
		name, value, assigned := strings.Cut(operand.Value, "=")
		switch {
		case spec.Name == "unset":
			frame.Unset(name)
		case assigned:
			err = frame.Assign(ctx, name, value)
			if err == nil {
				err = frame.Export(ctx, name)
			}
		default:
			err = frame.Export(ctx, name)
		}
		if err != nil {
			// Earlier operands stay applied; the frame never rolls back.
			return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func argvCode(err error) string {
	var argvErr *ArgvError
	if errors.As(err, &argvErr) {
		return argvErr.Code
	}
	return codeOperandInvalid
}
