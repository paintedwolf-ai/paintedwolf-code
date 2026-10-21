package projectpaths

import (
	"context"
	"os"
)

// directoryBinding freezes the logical coordinate of the actual held cwd,
// independently of its original native spelling (which may contain links/..).
func (p *ProgramPaths) directoryBinding(ctx context.Context, bound BoundPath) (BoundPath, os.FileInfo, error) {
	file, err := bound.OpenLaunchMetadata(true)
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

// OpenCwd returns a held directory only after checking the invocation's captured
// cwd identity. Launch adapters retain this handle while freezing launch inputs.
func (p *ProgramPaths) OpenCwd(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.cwd.Resolved.Abs == "" {
		return nil, invalidOperand("", "invocation cwd is not bound")
	}
	file, err := p.cwd.OpenLaunchMetadata(true)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !os.SameFile(info, p.cwdInfo) {
		err = invalidOperand(p.cwd.Resolved.DisplayPath, "invocation directory identity changed")
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
