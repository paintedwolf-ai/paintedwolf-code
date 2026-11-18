package projectsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// OpenOutsideSourceFile opens a canonical ledger address after caller admission.
func OpenOutsideSourceFile(rel string) (*os.File, error) {
	file, _, err := openOutsideFile(rel, "")
	return file, err
}

// OpenBoundOutsideSourceFile checks the parent identity on the read descriptor.
func OpenBoundOutsideSourceFile(rel, identity string) (*os.File, error) {
	file, _, err := openOutsideFile(rel, identity)
	return file, err
}

func openOutsideFile(rel, identity string) (*os.File, string, error) {
	if !filepath.IsLocal(rel) || rel == "." || filepath.Clean(rel) != rel {
		return nil, "", ErrSourcePathDenied
	}
	parent := filepath.Dir(projectroot.OutsideAbs(rel))
	root, err := fseffect.OpenReadRoot(parent)
	if err != nil {
		return nil, "", outsideOpenError(err)
	}
	defer func() { _ = root.Close() }()
	if root.Path() != parent {
		return nil, "", ErrSourcePathDenied
	}
	held, err := root.Open(".")
	if err != nil {
		return nil, "", outsideOpenError(err)
	}
	defer func() { _ = held.Close() }()
	parentID, err := fspath.FileIdentity(held)
	if err != nil {
		return nil, "", err
	}
	if identity != "" && identity != parentID {
		return nil, "", ErrSourcePathDenied
	}
	file, err := root.OpenCanonicalFile(filepath.Base(rel))
	return file, parentID, outsideOpenError(err)
}

func outsideOpenError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ErrSourceNotFound
	case errors.Is(err, fseffect.ErrSymlink), errors.Is(err, fseffect.ErrPredicateChanged):
		return ErrSourcePathDenied
	default:
		return err
	}
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
	file, parentID, err := openOutsideFile(path, "")
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
	return sourceReadTarget{abs: file.Name(), path: path, rootID: rootID, info: info, outsideParentIdentity: parentID}, file, nil
}
