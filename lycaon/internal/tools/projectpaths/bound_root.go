package projectpaths

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// openRoot checks the held directory, including replacement between path checks.
func (p BoundPath) openRoot() (*fseffect.ReadRoot, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	root, err := fseffect.OpenReadRoot(p.Resolved.Root.Path)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenExact(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	info, err := file.Stat()
	_ = file.Close()
	if err == nil && !os.SameFile(info, p.rootInfo) {
		err = invalidOperand(p.Resolved.DisplayPath, "held root identity changed")
	}
	if err == nil {
		err = p.Validate()
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

// PathOperationError preserves typed causes while classifying adapter and confinement failures.
func PathOperationError(operand string, err error) error {
	var reject *toolrejection.ToolReject
	var pathErr *PathError
	if errors.As(err, &reject) || errors.As(err, &pathErr) {
		return err
	}
	if errors.Is(err, fseffect.ErrUnsupported) {
		return &PathError{Code: "COMMAND_PLATFORM_UNAVAILABLE", Operand: operand, Cause: err}
	}
	var scope *sandbox.ScopeError
	if errors.Is(err, fseffect.ErrSymlink) || errors.Is(err, sandbox.ErrPathEscape) || errors.As(err, &scope) {
		return errors.Join(err, &toolrejection.ToolReject{Code: "SURVEY_PATH_ESCAPE", Data: map[string]any{"path": operand, "reason": err.Error()}})
	}
	return err
}

// OpenLaunchMetadata retains executable or cwd identity without source reads.
func (p BoundPath) OpenLaunchMetadata(directory bool) (*os.File, error) {
	if p.Null {
		return nil, invalidOperand(p.Resolved.DisplayPath, "null is not a metadata file")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	root, err := fseffect.OpenMetadataRoot(p.Resolved.Root.Path)
	if err != nil {
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	defer func() { _ = root.Close() }()
	held, err := root.OpenExact(".", true)
	if err != nil {
		return nil, err
	}
	info, err := held.Stat()
	_ = held.Close()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, p.rootInfo) {
		return nil, invalidOperand(p.Resolved.DisplayPath, "held root identity changed")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	file, err := root.OpenExact(p.exactRelative, directory)
	if err != nil {
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	if p.admitRead != nil {
		if err := p.admitRead(file.Name()); err != nil {
			_ = file.Close()
			return nil, PathOperationError(p.Resolved.DisplayPath, err)
		}
	}
	return file, nil
}
