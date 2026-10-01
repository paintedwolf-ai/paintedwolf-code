package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textfile"
)

var (
	// ErrSourcePermissionChange reports a host refusal to change file mode.
	ErrSourcePermissionChange = errors.New("source permission change failed")
)

// SourceMakeEditableRequest identifies bytes approved for a mode change.
type SourceMakeEditableRequest struct {
	RootID     string
	Path       string
	BaseSHA256 string
}

// SourceMakeEditableResult reports the applied permission transition.
type SourceMakeEditableResult struct {
	RootID       string
	Path         string
	PreviousMode uint32
	Mode         uint32
	Writable     bool
}

// MakeSourceEditable adds owner-write permission to verified source bytes.
func MakeSourceEditable(p *Project, req SourceMakeEditableRequest) (*SourceMakeEditableResult, error) {
	if p == nil || len(p.Roots) == 0 {
		return nil, ErrSourceNoRoot
	}
	pathQuery := strings.TrimSpace(req.Path)
	baseSHA := strings.ToLower(strings.TrimSpace(req.BaseSHA256))
	if pathQuery == "" || baseSHA == "" {
		return nil, ErrSourcePathInvalid
	}
	roots, err := scopedRootsForSource(p, req.RootID)
	if err != nil {
		return nil, err
	}
	var (
		root       Root
		rel        string
		normalized bool
	)
	for _, candidate := range roots {
		abs, candidateRel, ok := evidence.ResolveCitationAbs(candidate.Path, pathQuery)
		if !ok {
			continue
		}
		normalized = true
		info, statErr := os.Stat(abs)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, fmt.Errorf("stat source: %w", statErr)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		root, rel = candidate, candidateRel
		break
	}
	if rel == "" {
		if normalized {
			return nil, ErrSourceNotFound
		}
		return nil, ErrSourcePathDenied
	}

	modeResult, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
		Location: fseffect.Location{Root: root.Path, Rel: filepath.FromSlash(rel)},
		Update: func(mode os.FileMode) os.FileMode {
			return mode | 0o200
		},
		BeforeCommit: func(file *os.File, info os.FileInfo) error {
			if info.Size() > SourceReadMaxBytes {
				return ErrSourceWriteConflict
			}
			if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
				return fmt.Errorf("seek source: %w", seekErr)
			}
			current, readErr := io.ReadAll(io.LimitReader(file, SourceReadMaxBytes+1))
			if readErr != nil {
				return fmt.Errorf("read source: %w", readErr)
			}
			if len(current) > SourceReadMaxBytes || textfile.SHA256(current) != baseSHA {
				return ErrSourceWriteConflict
			}
			return nil
		},
	})
	if err != nil {
		if errors.Is(err, ErrSourceWriteConflict) {
			return nil, ErrSourceWriteConflict
		}
		if errors.Is(err, fseffect.ErrInvalidPath) || errors.Is(err, fseffect.ErrSymlink) {
			return nil, ErrSourcePathDenied
		}
		return nil, fmt.Errorf("%w: %w", ErrSourcePermissionChange, err)
	}
	return &SourceMakeEditableResult{
		RootID:       root.ID,
		Path:         rel,
		PreviousMode: uint32(modeResult.Before.Perm()),
		Mode:         uint32(modeResult.After.Perm()),
		Writable:     SourceFileWritable(uint32(modeResult.After.Perm())),
	}, nil
}
