package projectpaths

import (
	"context"
	"errors"
	"os"
)

// OpenExecutable captures a launch input through current read authority and the
// exact jailed path walker. It grants no external-execution capability; callers
// must also pass the existing command-window launch admission.
func (p *ProgramPaths) OpenExecutable(ctx context.Context, operand string) (*os.File, error) {
	bound, err := p.Bind(ctx, operand, false)
	if err != nil {
		return nil, err
	}
	if bound.Null {
		return nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Operand: operand, Cause: errors.New("null device is not an executable input")}
	}
	file, err := bound.OpenMetadata()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
