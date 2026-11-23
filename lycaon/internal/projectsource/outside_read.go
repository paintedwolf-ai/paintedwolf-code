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

// OpenOutsideSourceFile opens a canonical address after caller admission.
func OpenOutsideSourceFile(rel string, binding projectroot.OutsideBinding) (*os.File, error) {
	file, _, err := openOutsideFile(rel, binding)
	return file, err
}

func OpenBoundOutsideSourceFile(rel, identity string) (*os.File, error) {
	return OpenOutsideSourceFile(rel, parentBinding(rel, identity))
}

func parentBinding(rel, identity string) projectroot.OutsideBinding {
	return projectroot.OutsideBinding{Directory: filepath.Dir(projectroot.OutsideAbs(rel)), Identity: identity}
}

func sourceReadBinding(p ProjectSource) projectroot.OutsideBinding {
	if bound, ok := p.(interface {
		OutsideReadBinding() projectroot.OutsideBinding
	}); ok {
		return bound.OutsideReadBinding()
	}
	return projectroot.OutsideBinding{}
}

func openOutsideFile(rel string, binding projectroot.OutsideBinding) (*os.File, string, error) {
	if !filepath.IsLocal(rel) || rel == "." || filepath.Clean(rel) != rel {
		return nil, "", ErrSourcePathDenied
	}
	if binding.Directory == "" {
		binding = parentBinding(rel, "")
	}
	path, err := filepath.Rel(binding.Directory, projectroot.OutsideAbs(rel))
	if err != nil || !filepath.IsLocal(path) || path == "." {
		return nil, "", ErrSourcePathDenied
	}
	root, err := fseffect.OpenReadRoot(binding.Directory)
	if err != nil {
		return nil, "", outsideOpenError(err)
	}
	defer func() { _ = root.Close() }()
	if root.Path() != binding.Directory {
		return nil, "", ErrSourcePathDenied
	}
	if binding.Identity != "" {
		held, err := root.Open(".")
		if err != nil {
			return nil, "", outsideOpenError(err)
		}
		identity, err := fspath.FileIdentity(held)
		_ = held.Close()
		if err != nil {
			return nil, "", err
		}
		if identity != binding.Identity {
			return nil, "", ErrSourcePathDenied
		}
	}
	file, parentID, err := root.OpenCanonicalFile(path)
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
			return openOutsideSourceRead(path, rootID, sourceReadBinding(p))
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

func openOutsideSourceRead(path, rootID string, binding projectroot.OutsideBinding) (sourceReadTarget, *os.File, error) {
	file, parentID, err := openOutsideFile(path, binding)
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
