package projectpaths

import (
	"context"
	"os"
)

// directoryBinding freezes the logical coordinate of the actual held cwd,
// independently of its original native spelling (which may contain links/..).
func (p *ProgramPaths) directoryBinding(ctx context.Context, bound BoundPath) (BoundPath, os.FileInfo, error) {
	file, err := bound.Open()
	if err != nil {
		return BoundPath{}, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return BoundPath{}, nil, err
	}
	if !info.IsDir() {
		return BoundPath{}, nil, invalidOperand(bound.Resolved.DisplayPath, "directory required")
	}
	authority, err := authorityAddress(bound.Resolved.Root.Path, file.Name())
	if err != nil {
		return BoundPath{}, nil, err
	}
	logical, err := ResolveRead(ctx, p.boundary, p.context, authority)
	if err != nil {
		return BoundPath{}, nil, err
	}
	bound.Resolved.DisplayPath = logical.DisplayPath
	bound.Resolved.ScopeRel = logical.ScopeRel
	return bound, info, nil
}
