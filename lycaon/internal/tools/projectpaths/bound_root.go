package projectpaths

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
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
		root.Close()
		return nil, err
	}
	info, err := file.Stat()
	file.Close()
	if err == nil && !os.SameFile(info, p.rootInfo) {
		err = invalidOperand(p.Resolved.DisplayPath, "held root identity changed")
	}
	if err == nil {
		err = p.Validate()
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func pathOperationError(operand string, err error) error {
	if errors.Is(err, fseffect.ErrUnsupported) {
		return &PathError{Code: "COMMAND_PLATFORM_UNAVAILABLE", Operand: operand, Cause: err}
	}
	return err
}
