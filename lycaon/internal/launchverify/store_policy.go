package launchverify

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// StorePolicy is an administrator-selected closure. Neither a store prefix nor
// root ownership alone adds an executable object to this set.
type StorePolicy struct {
	directory string
	outputs   map[string]struct{}
}

// NewStorePolicy accepts exact output roots, not globs or historical prefixes.
func NewStorePolicy(directory string, outputs []string) (*StorePolicy, error) {
	if !cleanAbsolute(directory) || directory == "/" || len(outputs) == 0 || len(outputs) > 100000 {
		return nil, errors.New("invalid selected store or closure size")
	}
	policy := &StorePolicy{directory: directory, outputs: make(map[string]struct{}, len(outputs))}
	for _, output := range outputs {
		if !cleanAbsolute(output) || filepath.Dir(output) != directory {
			return nil, fmt.Errorf("closure output %q is not directly in the selected store", output)
		}
		if _, exists := policy.outputs[output]; exists {
			return nil, errors.New("duplicate closure output")
		}
		policy.outputs[output] = struct{}{}
	}
	return policy, nil
}

func cleanAbsolute(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }

func (policy *StorePolicy) contains(path string) bool {
	if !cleanAbsolute(path) {
		return false
	}
	relative, err := filepath.Rel(policy.directory, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return false
	}
	first, _, _ := strings.Cut(relative, string(filepath.Separator))
	_, ok := policy.outputs[filepath.Join(policy.directory, first)]
	return ok
}

func (policy *StorePolicy) verifyMapping(stat func(string) (fileFacts, error), region mapping) error {
	if !policy.contains(region.path) {
		return fmt.Errorf("%s is outside the administrator-selected closure", region.path)
	}
	facts, err := stat(region.path)
	if err != nil {
		return err
	}
	if !facts.mode.IsRegular() || facts.inode != region.inode {
		return errors.New("selected mapping is no longer the mapped regular file")
	}
	return policy.verifyPath(stat, region.path)
}

// VerifyConfigurationFile checks a root-controlled trust file and its ancestors.
// Store files are immutable; an explicit /etc trust file may remain root-writable.
func (policy *StorePolicy) VerifyConfigurationFile(path string) error {
	return policy.verifyPath(lstatFacts, path)
}

func (policy *StorePolicy) verifyPath(stat func(string) (fileFacts, error), path string) error {
	if !cleanAbsolute(path) {
		return errors.New("trust path must be absolute and clean")
	}
	insideStore := strings.HasPrefix(path, policy.directory+string(filepath.Separator))
	for current := path; ; current = filepath.Dir(current) {
		facts, err := stat(current)
		if err != nil {
			return fmt.Errorf("%s: %w", current, err)
		}
		if current == path {
			if !facts.mode.IsRegular() {
				return errors.New("trust object is not a regular file")
			}
		} else if !facts.mode.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
		if current == policy.directory {
			if facts.uid != 0 || !facts.mode.IsDir() || facts.mode.Perm()&0o002 != 0 || (facts.mode.Perm()&0o020 != 0 && facts.mode&fs.ModeSticky == 0) {
				return errors.New("selected store directory must be root-owned and sticky when group-writable")
			}
			insideStore = false
		} else {
			if err := rootOwned(current, facts); err != nil {
				return err
			}
			if insideStore && facts.mode.Perm()&0o222 != 0 {
				return fmt.Errorf("%s is not an immutable store object", current)
			}
		}
		if current == "/" {
			return nil
		}
	}
}
