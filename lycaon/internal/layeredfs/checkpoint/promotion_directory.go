package checkpoint

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// A directory's own stat does not prove that removing its children is reviewed.
// Every live descendant must have its own matching, deleted checkpoint base.
func (pr *Promoter) observeRemovedDirectory(m Manifest, parent layeredfs.QualifiedPath) (map[string]TargetObservation, error) {
	entries := map[layeredfs.QualifiedPath]ManifestEntry{}
	for _, entry := range m.Entries {
		entries[entry.Path] = entry
	}
	root, err := pr.targets.openTargetRoot(parent.RootID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	observed := map[string]TargetObservation{}
	var visit func(layeredfs.QualifiedPath) (bool, error)
	visit = func(path layeredfs.QualifiedPath) (bool, error) {
		directory, err := root.Open(path.Rel)
		if err != nil {
			return false, err
		}
		children, err := directory.ReadDir(-1)
		_ = directory.Close()
		if err != nil {
			return false, err
		}
		for _, child := range children {
			childPath := layeredfs.QualifiedPath{RootID: path.RootID, Rel: filepath.Join(path.Rel, child.Name())}
			entry, known := entries[childPath]
			if !known || (!entry.Deleted && entry.Kind != layeredfs.EntryAbsent) {
				return false, nil
			}
			current, err := pr.targets.observeTarget(childPath)
			if err != nil {
				return false, err
			}
			if !targetMatchesBase(current, entry.Base) || (entry.Base.Mode != 0 && entry.Base.Mode.Perm() != current.Mode.Perm()) {
				return false, nil
			}
			observed[pathKey(childPath)] = current
			if current.Kind == layeredfs.EntryDirectory {
				valid, err := visit(childPath)
				if err != nil || !valid {
					return valid, err
				}
			}
		}
		return true, nil
	}
	valid, err := visit(parent)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, nil
	}
	return observed, nil
}
