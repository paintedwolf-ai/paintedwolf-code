package projectpaths

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// OpenSharedRoot validates and opens the held directory root for reuse across a walk.
func (p BoundPath) OpenSharedRoot() (*fseffect.ReadRoot, error) {
	if p.Null {
		return nil, invalidOperand(p.Resolved.DisplayPath, "null device is not a directory entry")
	}
	return p.openRoot()
}

// AdmitPathRead rechecks read admission for actual under the bound authority.
func (p BoundPath) AdmitPathRead(actual string) error {
	if p.Null {
		return invalidOperand(p.Resolved.DisplayPath, "null input is an empty stream, not a file")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.admitRead != nil {
		return PathOperationError(p.Resolved.DisplayPath, p.admitRead(actual))
	}
	return nil
}

// OpenWithRoot reads through shared when non-nil, rechecking bound identity and read admission.
func (p BoundPath) OpenWithRoot(shared *fseffect.ReadRoot) (*os.File, error) {
	if p.Null {
		return nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Cause: errors.New("null input is an empty stream, not a file")}
	}
	if settingsoverlay.IsRecoveryRelPath(p.Resolved.ScopeRel) || settingsoverlay.HasRecoverySegment(p.Resolved.Abs) || settingsoverlay.IsRecoveryOnlyDir(p.Resolved.Abs) {
		return nil, PathOperationError(p.Resolved.DisplayPath, &os.PathError{Op: "open", Path: p.Resolved.DisplayPath, Err: os.ErrNotExist})
	}
	root := shared
	ownsRoot := false
	if root != nil {
		if err := p.Validate(); err != nil {
			return nil, PathOperationError(p.Resolved.DisplayPath, err)
		}
	} else {
		var err error
		root, err = p.openRoot()
		if err != nil {
			return nil, PathOperationError(p.Resolved.DisplayPath, err)
		}
		ownsRoot = true
	}
	closeRoot := func() {
		if ownsRoot {
			_ = root.Close()
		}
	}
	file, err := root.OpenExact(p.exactRelative)
	if err != nil {
		closeRoot()
		return nil, PathOperationError(p.Resolved.DisplayPath, err)
	}
	if p.admitRead != nil {
		if err := p.admitRead(file.Name()); err != nil {
			_ = file.Close()
			closeRoot()
			return nil, PathOperationError(p.Resolved.DisplayPath, err)
		}
	}
	closeRoot()
	return file, nil
}

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
		return nil, errors.Join(err, root.Close())
	}
	info, err := file.Stat()
	err = errors.Join(err, file.Close())
	if err == nil && !os.SameFile(info, p.rootInfo) {
		err = invalidOperand(p.Resolved.DisplayPath, "held root identity changed")
	}
	if err == nil {
		err = p.Validate()
	}
	if err != nil {
		return nil, errors.Join(err, root.Close())
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
