package projectsource

import (
	"bytes"
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

// SourceWriteMaxBytes matches the source read cap.
const SourceWriteMaxBytes = SourceReadMaxBytes

var (
	// ErrSourceWriteConflict reports a stale base hash.
	ErrSourceWriteConflict = errors.New("source write conflict")
	// ErrSourceWriteTooLarge reports content over the write cap.
	ErrSourceWriteTooLarge = errors.New("source write too large")
	// ErrSourceEncodingInvalid reports an unsupported representation.
	ErrSourceEncodingInvalid = errors.New("source encoding invalid")
)

// SourceWriteRequest is one guarded project file save.
type SourceWriteRequest struct {
	OutsideParentIdentity string
	Path                  string
	// RootID pins the write to one attached root.
	RootID   string
	Content  string
	Encoding string
	// BaseSHA256 guards the atomic replacement.
	BaseSHA256 string
	SessionID  string
	Turn       int
}

// SourceWriteResult reports a completed save.
type SourceWriteResult struct {
	Path      string
	RootID    string
	AbsPath   string
	SizeBytes int64
	SHA256    string
	Before    []byte
	After     []byte
	Changed   bool
}

type sourceWritePlan struct {
	OutsideParentIdentity string
	Result                SourceWriteResult
	BaseSHA256            string
	// RootPath anchors the descriptor-relative commit walk.
	RootPath string
}

// ApplySourceWriteCAS atomically replaces a guarded file.
func ApplySourceWriteCAS(p ProjectSource, req SourceWriteRequest) (*SourceWriteResult, error) {
	plan, err := planProjectSourceWrite(p, req)
	if err != nil {
		return nil, err
	}
	if err := applyProjectSourceWrite(plan); err != nil {
		return nil, err
	}
	result := plan.Result
	return &result, nil
}

// planProjectSourceWrite captures a guarded before/after image.
func planProjectSourceWrite(p ProjectSource, req SourceWriteRequest) (*sourceWritePlan, error) {
	if p == nil {
		return nil, ErrSourceNoRoot
	}
	pathQuery := strings.TrimSpace(req.Path)
	if pathQuery == "" {
		return nil, ErrSourcePathInvalid
	}
	base := strings.ToLower(strings.TrimSpace(req.BaseSHA256))
	if base == "" {
		return nil, ErrSourcePathInvalid
	}
	if len(p.SourceRoots()) == 0 {
		return nil, ErrSourceNoRoot
	}
	next, err := textfile.EncodeBounded(req.Content, req.Encoding,
		textfile.LimitsForRaw(SourceWriteMaxBytes))
	if errors.Is(err, textfile.ErrRawTooLarge) || errors.Is(err, textfile.ErrTextTooLarge) {
		return nil, ErrSourceWriteTooLarge
	}
	if errors.Is(err, textfile.ErrBinary) {
		return nil, ErrSourceBinary
	}
	if err != nil {
		return nil, ErrSourceEncodingInvalid
	}

	roots, err := scopedRootsForSource(p, req.RootID)
	if err != nil {
		return nil, err
	}
	var (
		abs        string
		rel        string
		rootID     string
		rootPath   string
		normalized bool
	)
	for _, root := range roots {
		a, r, ok := evidence.ResolveCitationAbs(root.Path, pathQuery)
		if !ok {
			continue
		}
		normalized = true
		if root.Outside {
			if a != filepath.Join(root.Path, filepath.FromSlash(pathQuery)) {
				return nil, ErrSourcePathDenied
			}
			abs, rel, rootID, rootPath = a, r, root.ID, root.Path
			break
		}
		info, err := os.Stat(a)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat source: %w", err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		abs = a
		rel = r
		rootID = root.ID
		rootPath = root.Path
		break
	}
	if abs == "" {
		if normalized {
			return nil, ErrSourceNotFound
		}
		return nil, ErrSourcePathDenied
	}

	current, err := readSourceWritePreimage(abs, rel, req.OutsideParentIdentity)
	if err != nil {
		return nil, err
	}
	currentDoc, _, err := textfile.Open(current, textfile.LimitsForRaw(SourceReadMaxBytes))
	if err != nil && (req.Encoding == textfile.UTF16LE || req.Encoding == textfile.UTF16BE) {
		currentDoc, _, err = textfile.OpenAsUTF16WithoutBOM(current, req.Encoding, textfile.LimitsForRaw(SourceReadMaxBytes))
	}
	if err != nil {
		return nil, ErrSourceEncodingInvalid
	}
	if currentDoc.Encoding() != req.Encoding {
		return nil, ErrSourceEncodingInvalid
	}
	if bytes.Equal(current, next) {
		return &sourceWritePlan{Result: SourceWriteResult{
			Path: rel, RootID: rootID, AbsPath: abs, SizeBytes: int64(len(next)), SHA256: textfile.SHA256(next),
			Before: current, After: next,
		}, BaseSHA256: base, RootPath: rootPath, OutsideParentIdentity: req.OutsideParentIdentity}, nil
	}
	if textfile.SHA256(current) != base {
		return nil, ErrSourceWriteConflict
	}
	return &sourceWritePlan{Result: SourceWriteResult{
		Path: rel, RootID: rootID, AbsPath: abs, SizeBytes: int64(len(next)), SHA256: textfile.SHA256(next),
		Before: current, After: next, Changed: true,
	}, BaseSHA256: base, RootPath: rootPath, OutsideParentIdentity: req.OutsideParentIdentity}, nil
}

// applyProjectSourceWrite converges on a prepared after-image.
func applyProjectSourceWrite(plan *sourceWritePlan) error {
	if plan == nil {
		return ErrSourcePathInvalid
	}
	if !plan.Result.Changed {
		return nil
	}
	current, err := readSourceWritePreimage(plan.Result.AbsPath, plan.Result.Path, plan.OutsideParentIdentity)
	if err != nil {
		return err
	}
	currentSHA := textfile.SHA256(current)
	if currentSHA == plan.Result.SHA256 {
		return nil
	}
	if currentSHA != plan.BaseSHA256 {
		return ErrSourceWriteConflict
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location:               fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Result.Path)},
		ExpectedParentIdentity: plan.OutsideParentIdentity,
		Source:                 bytes.NewReader(plan.Result.After),
		Mode:                   0o644,
		PreserveMode:           true,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			f, err := target.Open()
			if err != nil {
				return ErrSourceWriteConflict
			}
			defer func() { _ = f.Close() }()
			held, err := io.ReadAll(f)
			if err != nil || textfile.SHA256(held) != plan.BaseSHA256 {
				return ErrSourceWriteConflict
			}
			return nil
		},
	}); err != nil {
		if errors.Is(err, ErrSourceWriteConflict) || errors.Is(err, fseffect.ErrPredicateChanged) {
			return ErrSourceWriteConflict
		}
		return fmt.Errorf("write source: %w", err)
	}
	return nil
}

func readSourceWritePreimage(abs, rel, outsideIdentity string) ([]byte, error) {
	var f *os.File
	var err error
	if outsideIdentity != "" {
		f, err = OpenBoundOutsideSourceFile(rel, outsideIdentity)
	} else {
		f, err = os.Open(abs)
	}
	if err != nil {
		return nil, outsideOpenError(err)
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(io.LimitReader(f, SourceReadMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > SourceReadMaxBytes {
		return nil, ErrSourceWriteConflict
	}
	return content, nil
}
