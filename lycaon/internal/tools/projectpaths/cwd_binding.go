package projectpaths

import (
	"context"
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

// directoryBinding freezes the logical coordinate of the actual held cwd,
// independently of its original native spelling (which may contain links/..).
func (p *ProgramPaths) directoryBinding(ctx context.Context, bound BoundPath) (BoundPath, os.FileInfo, error) {
	file, err := bound.OpenLaunchMetadata(true)
	if err != nil {
		return BoundPath{}, nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return BoundPath{}, nil, err
	}
	if !info.IsDir() {
		return BoundPath{}, nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Operand: bound.Resolved.DisplayPath, Cause: errNotDirectory}
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
	file, err := p.bindAuthority(ctx, p.cwd, false).OpenLaunchMetadata(true)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !os.SameFile(info, p.cwdInfo) {
		err = invalidOperand(p.cwd.Resolved.DisplayPath, "invocation directory identity changed")
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// callCwdReject reports the command call's cwd field refusal under its own
// repair code, keeping the binding refusal's code; cancellation stays a
// cancellation.
func callCwdReject(ctx context.Context, code, cwd string, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	data := map[string]any{"cwd": cwd}
	var binding *toolrejection.ToolReject
	if errors.As(cause, &binding) {
		if binding.Code == "PROJECT_HAS_NO_ROOTS" {
			return binding
		}
		data["binding_code"] = binding.Code
	}
	return &toolrejection.ToolReject{Code: code, Data: data}
}
