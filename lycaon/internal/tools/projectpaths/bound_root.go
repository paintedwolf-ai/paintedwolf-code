package projectpaths

import (
	"github.com/lycaon/lycaon/internal/fseffect"
	"os"
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
