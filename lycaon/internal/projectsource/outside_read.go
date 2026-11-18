package projectsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// OpenOutsideSourceFile opens a canonical ledger address after caller admission.
func OpenOutsideSourceFile(rel string) (*os.File, error) {
	if !filepath.IsLocal(rel) || rel == "." || filepath.Clean(rel) != rel {
		return nil, ErrSourcePathDenied
	}
	root, err := fseffect.OpenReadRoot(projectroot.OutsidePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenCanonicalFile(rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSourceNotFound
	}
	if errors.Is(err, fseffect.ErrSymlink) || errors.Is(err, fseffect.ErrPredicateChanged) {
		return nil, ErrSourcePathDenied
	}
	return file, err
}

func openSourceRead(p ProjectSource, path, rootID string) (sourceReadTarget, *os.File, error) {
	for _, root := range p.SourceRoots() {
		if root.Outside && root.ID == rootID && rootID != "" {
			return openOutsideSourceRead(path, rootID)
		}
	}
	target, err := resolveSourceReadTarget(p, strings.TrimSpace(path), rootID)
	if err != nil {
		return sourceReadTarget{}, nil, err
	}
	file, err := os.Open(target.abs)
	if errors.Is(err, os.ErrNotExist) {
		return sourceReadTarget{}, nil, ErrSourceNotFound
	}
	if err != nil {
		return sourceReadTarget{}, nil, fmt.Errorf("open source: %w", err)
	}
	return target, file, nil
}

func openOutsideSourceRead(path, rootID string) (sourceReadTarget, *os.File, error) {
	file, err := OpenOutsideSourceFile(path)
	if err != nil {
		return sourceReadTarget{}, nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrSourceNotFound
	}
	if err != nil {
		_ = file.Close()
		return sourceReadTarget{}, nil, err
	}
	return sourceReadTarget{abs: file.Name(), path: path, rootID: rootID, info: info}, file, nil
}
