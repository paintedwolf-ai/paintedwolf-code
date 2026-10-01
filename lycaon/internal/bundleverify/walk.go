package bundleverify

import (
	"io/fs"
	"path/filepath"
	"sort"
)

// WalkMachO returns sorted facts without following symlinks.
func WalkMachO(root string) ([]MachOFacts, error) {
	var out []MachOFacts

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		facts, err := InspectMachO(path)
		if err != nil {
			return err
		}
		if facts != nil {
			out = append(out, *facts)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
