package projectpaths

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// Canonical resolves only through the confined filesystem reader. Like the
// version 1 realpath/readlink -f contract, every component must exist.
func (p *ProgramPaths) Canonical(ctx context.Context, operand string) (string, error) {
	return p.inspectPath(ctx, operand, true)
}

// LinkTarget reads link metadata without authorizing access to its target.
func (p *ProgramPaths) LinkTarget(ctx context.Context, operand string) (string, error) {
	return p.inspectPath(ctx, operand, false)
}

func (p *ProgramPaths) inspectPath(ctx context.Context, operand string, canonical bool) (string, error) {
	bound, err := p.Bind(ctx, operand, false)
	if err != nil {
		return "", err
	}
	if bound.Null {
		return "", invalidOperand(operand, "null device is not a directory entry")
	}
	root, err := bound.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	if canonical {
		actual, err := root.Canonical(bound.exactRelative)
		if err != nil {
			return "", err
		}
		if bound.admitRead != nil {
			if err := bound.admitRead(actual); err != nil {
				return "", err
			}
		}
		return actual, nil
	}
	target, err := root.Readlink(bound.exactRelative, bound.admitRead)
	if errors.Is(err, fseffect.ErrUnsupported) {
		return "", &PathError{Code: "COMMAND_PLATFORM_UNAVAILABLE", Operand: operand, Cause: err}
	}
	return target, err
}
