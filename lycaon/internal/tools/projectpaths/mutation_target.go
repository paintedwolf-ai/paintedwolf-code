package projectpaths

import (
	"path/filepath"
	"strings"
)

// MutationTarget resolves an exact parent immediately before the existing
// write door runs. It checks the shared mutation floor without recording or
// publishing effects. Callers must publish and record through that door.
func (p BoundPath) MutationTarget() (Resolved, error) {
	if p.Null || p.admitMutation == nil {
		return Resolved{}, invalidOperand(p.Resolved.DisplayPath, "ordinary mutation path binding required")
	}
	split := strings.LastIndexByte(p.exactRelative, byte(filepath.Separator))
	parent, name := ".", p.exactRelative
	if split >= 0 {
		parent, name = p.exactRelative[:split], p.exactRelative[split+1:]
	}
	if name == "" || name == "." || name == ".." {
		return Resolved{}, invalidOperand(p.Resolved.DisplayPath, "mutation requires a leaf entry")
	}
	root, err := p.openRoot()
	if err != nil {
		return Resolved{}, pathOperationError(p.Resolved.DisplayPath, err)
	}
	defer root.Close()
	actual, err := root.Canonical(parent)
	if err != nil {
		return Resolved{}, pathOperationError(p.Resolved.DisplayPath, err)
	}
	return p.admitMutation(filepath.Join(actual, name))
}
