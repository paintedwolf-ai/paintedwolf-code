package projectpaths

import (
	"context"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// Canonical resolves an existing operand's links through the confined
// filesystem reader; every component must exist.
func (p *ProgramPaths) Canonical(ctx context.Context, operand string) (string, error) {
	actual, _, err := p.canonicalAddress(ctx, operand)
	return actual, err
}

// canonicalAddress is Canonical plus the address that names the same entry
// under its attached root's own spelling, which binds further components.
func (p *ProgramPaths) canonicalAddress(ctx context.Context, operand string) (string, string, error) {
	bound, root, err := p.openBound(ctx, operand)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = root.Close() }()
	actual, err := root.Canonical(bound.exactRelative)
	if err != nil {
		return "", "", PathOperationError(operand, err)
	}
	if bound.admitRead != nil {
		if err := bound.admitRead(actual); err != nil {
			return "", "", PathOperationError(operand, err)
		}
	}
	address, err := authorityAddressWithCanonical(bound.Resolved.Root.Path, "", actual)
	if err != nil {
		return "", "", PathOperationError(operand, err)
	}
	return actual, address, nil
}

// LinkTarget reads link metadata without authorizing access to its target.
func (p *ProgramPaths) LinkTarget(ctx context.Context, operand string) (string, error) {
	bound, root, err := p.openBound(ctx, operand)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	target, err := root.Readlink(bound.exactRelative, bound.admitRead)
	return target, PathOperationError(operand, err)
}

// openBound binds a read operand and opens its held root.
func (p *ProgramPaths) openBound(ctx context.Context, operand string) (BoundPath, *fseffect.ReadRoot, error) {
	bound, err := p.Bind(ctx, operand, false)
	if err != nil {
		return BoundPath{}, nil, PathOperationError(operand, err)
	}
	if bound.Null {
		return BoundPath{}, nil, invalidOperand(operand, "null device is not a directory entry")
	}
	root, err := bound.openRoot()
	if err != nil {
		return BoundPath{}, nil, PathOperationError(operand, err)
	}
	return bound, root, nil
}
