package project

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textfile"
)

// ApplySourceWriteCreate publishes text at a path that must still be free.
// An occupied target is a conflict, never a replacement.
func ApplySourceWriteCreate(p *Project, req SourceWriteRequest) (*SourceWriteResult, error) {
	if p == nil {
		return nil, ErrSourceNoRoot
	}
	next, err := textfile.EncodeBounded(req.Content, req.Encoding, textfile.LimitsForRaw(SourceWriteMaxBytes))
	if errors.Is(err, textfile.ErrRawTooLarge) || errors.Is(err, textfile.ErrTextTooLarge) {
		return nil, ErrSourceWriteTooLarge
	}
	if errors.Is(err, textfile.ErrBinary) {
		return nil, ErrSourceBinary
	}
	if err != nil {
		return nil, ErrSourceEncodingInvalid
	}
	root, err := selectSingleSourceRoot(p, req.RootID)
	if err != nil {
		return nil, err
	}
	abs, rel, ok := evidence.ResolveCitationAbs(root.Path, req.Path)
	if !ok {
		return nil, ErrSourcePathDenied
	}
	if isReservedSourceRel(rel) {
		return nil, ErrSourcePathProtected
	}
	if _, err := os.Lstat(abs); err == nil {
		return nil, ErrSourceWriteConflict
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat source: %w", err)
	}
	location := fseffect.Location{Root: root.Path, Rel: filepath.FromSlash(rel)}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: location,
		Source:   bytes.NewReader(next),
		Mode:     sourceCreateFileMode,
		DirMode:  sourceCreateDirMode,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			// The target must still be free under the parent lock.
			if _, err := target.Lstat(); err == nil {
				return ErrSourceWriteConflict
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat source: %w", err)
			}
			return nil
		},
	}); err != nil {
		if errors.Is(err, ErrSourceWriteConflict) {
			return nil, ErrSourceWriteConflict
		}
		return nil, fmt.Errorf("create source: %w", err)
	}
	return &SourceWriteResult{
		Path: rel, RootID: root.ID, AbsPath: abs, SizeBytes: int64(len(next)), SHA256: textfile.SHA256(next),
		Before: nil, After: next, Changed: true,
	}, nil
}
