package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// ExecutionRevisions captures and rechecks the program-specific revisions that
// native utilities read and publish through. Native reads always capture
// published disk; destinations with unsaved editor drafts refuse with
// COMMAND_REVISION_CONFLICT.
type ExecutionRevisions struct {
	Boundary *sandbox.Boundary
}

// ExecutionSource is one pinned native input: the exact text the utility
// transforms and the revision a later publication rechecks.
type ExecutionSource struct {
	Path     string
	Content  string
	Revision toolcontract.ExecutionRevision
}

// ExecutionRevisionConflict reports that a pinned source or destination moved
// after capture, or that a destination holds an unsaved editor draft. The
// prepared change is stopped, never refreshed or rerun.
type ExecutionRevisionConflict struct {
	rejectOnce   sync.Once
	reject       *toolrejection.ToolReject
	Path         string
	Pinned       toolcontract.ExecutionRevision
	Current      toolcontract.ExecutionRevision
	UnsavedDraft bool
	Cause        error
}

func (c *ExecutionRevisionConflict) Error() string {
	if c.UnsavedDraft {
		return fmt.Sprintf("program destination %s has unsaved changes in the editor", c.Path)
	}
	return fmt.Sprintf("program %s %s changed since it was captured", c.Pinned.Role, c.Path)
}

func (c *ExecutionRevisionConflict) Unwrap() []error {
	c.rejectOnce.Do(func() {
		data := map[string]any{"path": c.Path}
		if expected, observed := c.Pinned.Identity(), c.Current.Identity(); expected != "" && observed != "" && expected != observed {
			data["expected_revision"], data["observed_revision"] = expected, observed
		}
		if c.UnsavedDraft {
			data["unsaved_draft"] = true
		}
		c.reject = &toolrejection.ToolReject{Code: "COMMAND_REVISION_CONFLICT", Data: data}
	})
	if c.Cause == nil {
		return []error{c.reject}
	}
	return []error{c.reject, c.Cause}
}

// CaptureSource pins one native input from published disk before the transform
// that reads it.
func (e ExecutionRevisions) CaptureSource(ctx context.Context, tctx tools.ToolContext, path string) (ExecutionSource, error) {
	resolved, err := projectpaths.ResolveRead(ctx, e.Boundary, tctx, path)
	if err != nil {
		return ExecutionSource{}, err
	}
	if err := tools.ObserveCommandFileRead(ctx, resolved.Root.ID, resolved.ScopeRel); err != nil {
		return ExecutionSource{}, err
	}
	st, revision, err := captureSourceDisk(tctx, resolved)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
			return ExecutionSource{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
		}
		return ExecutionSource{}, err
	}
	return ExecutionSource{Path: resolved.DisplayPath, Content: st.Content, Revision: revision}, nil
}

// CaptureDestination pins a destination's revision, or its absence, before
// the program prepares the bytes it will publish there.
func (e ExecutionRevisions) CaptureDestination(ctx context.Context, tctx tools.ToolContext, path string) (toolcontract.ExecutionRevision, error) {
	resolved, err := projectpaths.ResolveWrite(ctx, e.Boundary, tctx, path)
	if err != nil {
		return toolcontract.ExecutionRevision{}, err
	}
	_, revision, err := captureRevision(ctx, tctx, resolved, toolcontract.DestinationRevision)
	return revision, err
}

// DeclareLaunchInput records the published bytes an external process may read
// at launch. It states disk provenance, not an observation of what it read.
func (e ExecutionRevisions) DeclareLaunchInput(ctx context.Context, tctx tools.ToolContext, path string) (toolcontract.ExecutionRevision, error) {
	resolved, err := projectpaths.ResolveRead(ctx, e.Boundary, tctx, path)
	if err != nil {
		return toolcontract.ExecutionRevision{}, err
	}
	f, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return toolcontract.ExecutionRevision{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
		}
		return toolcontract.ExecutionRevision{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return toolcontract.ExecutionRevision{}, err
	}
	if !info.Mode().IsRegular() {
		return toolcontract.ExecutionRevision{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return toolcontract.ExecutionRevision{}, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	root, rel := revisionLocation(resolved)
	return toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.DeclaredAtLaunch,
		RootID: root, Path: rel, DiskSHA256: digest, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: digest, ContentBytes: size}, nil
}

// Verify recaptures each pin and conflicts on the first that moved. It is the
// only freshness check a pinned operation gets: there is no automatic retry.
func (e ExecutionRevisions) Verify(ctx context.Context, tctx tools.ToolContext, pins ...toolcontract.ExecutionRevision) error {
	for _, pin := range pins {
		abs, ok := pinnedAbs(tctx, pin)
		if !ok {
			return &ExecutionRevisionConflict{Path: pin.Path, Pinned: pin}
		}
		resolve := projectpaths.ResolveRead
		if pin.Role == toolcontract.DestinationRevision {
			resolve = projectpaths.ResolveWrite
		}
		resolved, err := resolve(ctx, e.Boundary, tctx, abs)
		if err != nil {
			return err
		}
		var current toolcontract.ExecutionRevision
		if pin.Basis == toolcontract.CapturedRevision && pin.Representation == toolcontract.OriginalEncodedBytes {
			current, err = recaptureBytes(ctx, tctx, resolved)
		} else if pin.Role == toolcontract.SourceRevision {
			_, current, err = captureSourceDisk(tctx, resolved)
		} else {
			_, current, err = captureRevision(ctx, tctx, resolved, pin.Role)
		}
		if errors.Is(err, fs.ErrNotExist) || toolrejection.AsToolReject(err) != nil {
			var draftConflict *ExecutionRevisionConflict
			if errors.As(err, &draftConflict) {
				return draftConflict
			}
			return &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: pin, Cause: err}
		}
		if err != nil {
			return err
		}
		if !current.Same(pin) {
			return &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: pin, Current: current}
		}
	}
	return nil
}

func captureSourceDisk(tctx tools.ToolContext, resolved projectpaths.Resolved) (sourceview.Text, toolcontract.ExecutionRevision, error) {
	root, rel := revisionLocation(resolved)
	revision := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel, Representation: toolcontract.UTF8SourceBytes}
	disk, err := sourceview.ReadTextDocument("read", sourceview.AccessRead, resolved, tctx.Host.MaxToolSpillBytes)
	if err != nil {
		return sourceview.Text{}, toolcontract.ExecutionRevision{}, err
	}
	content := disk.Text()
	revision.DiskSHA256 = disk.RawSHA256()
	revision.ContentSHA256, revision.ContentBytes = textfile.SHA256([]byte(content)), int64(len(content))
	return sourceview.Text{Content: content, Disk: &disk}, revision, revision.Validate()
}

func unsavedDraftConflict(resolved projectpaths.Resolved, doc tools.EditorDocumentState) *ExecutionRevisionConflict {
	root, rel := revisionLocation(resolved)
	pinned := toolcontract.ExecutionRevision{Role: toolcontract.DestinationRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel, DiskSHA256: doc.BaseSHA256, ContentSHA256: doc.BaseSHA256}
	if doc.Absent || doc.BaseSHA256 == "" {
		pinned.Mode = toolcontract.AbsentRevision
	}
	current := toolcontract.ExecutionRevision{Role: toolcontract.DestinationRevision, Mode: toolcontract.AcceptedDocumentRevision, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel, DocumentID: doc.ID, DocumentRevision: doc.Revision, DiskSHA256: doc.BaseSHA256, ContentSHA256: ""}
	return &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: pinned, Current: current, UnsavedDraft: true}
}

func captureRevision(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, role toolcontract.ExecutionRevisionRole) (sourceview.Text, toolcontract.ExecutionRevision, error) {
	if role == toolcontract.SourceRevision {
		return captureSourceDisk(tctx, resolved)
	}
	root, rel := revisionLocation(resolved)
	revision := toolcontract.ExecutionRevision{Role: role, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel}
	st, err := sourceview.LoadText(ctx, "write", sourceview.AccessMutate, tctx, resolved)
	if rawDestination(err) {
		return captureRawRevision(resolved, revision)
	}
	if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
		revision.Mode = toolcontract.AbsentRevision
		return sourceview.Text{}, revision, nil
	}
	if err != nil {
		return sourceview.Text{}, toolcontract.ExecutionRevision{}, err
	}
	if st.Editor != nil && (st.Editor.Dirty || st.Editor.Diverged) {
		return sourceview.Text{}, toolcontract.ExecutionRevision{}, unsavedDraftConflict(resolved, tools.EditorDocumentState{ID: st.Editor.ID, RootID: resolved.Root.ID, Path: resolved.ScopeRel, Revision: st.Editor.Revision, BaseSHA256: st.Editor.BaseSHA256, Absent: st.Editor.Absent})
	}
	revision.ContentSHA256, revision.ContentBytes = textfile.SHA256([]byte(st.Content)), int64(len(st.Content))
	if st.Editor != nil {
		revision.Mode, revision.Representation = toolcontract.AcceptedDocumentRevision, toolcontract.LogicalUTF8LF
		revision.DocumentID, revision.DocumentRevision, revision.DiskSHA256 = st.Editor.ID, st.Editor.Revision, st.Editor.BaseSHA256
	} else {
		revision.Mode, revision.Representation = toolcontract.PublishedDiskRevision, toolcontract.UTF8SourceBytes
		revision.DiskSHA256 = st.Disk.RawSHA256()
	}
	return st, revision, revision.Validate()
}

// revisionLocation names a path by its attached root, or absolutely when it
// was granted outside every root.
func revisionLocation(resolved projectpaths.Resolved) (string, string) {
	if resolved.External || resolved.Root.ID == "" {
		return "", resolved.Abs
	}
	return resolved.Root.ID, resolved.ScopeRel
}

func pinnedAbs(tctx tools.ToolContext, pin toolcontract.ExecutionRevision) (string, bool) {
	if pin.RootID == "" {
		return pin.Path, filepath.IsAbs(pin.Path)
	}
	for _, root := range tctx.Source.Roots {
		if root.ID == pin.RootID {
			return filepath.Join(root.Path, filepath.FromSlash(pin.Path)), true
		}
	}
	return "", false
}

// sameLocation reports whether a resolved path is the one a pin captured.
func sameLocation(resolved projectpaths.Resolved, pin toolcontract.ExecutionRevision) bool {
	root, rel := revisionLocation(resolved)
	return root == pin.RootID && rel == pin.Path
}

// rawDestination reports a destination that has no editable text view: binary
// bytes or a file above the text door. Its revision is its exact disk bytes.
func rawDestination(err error) bool {
	reject := toolrejection.AsToolReject(err)
	return reject != nil && (reject.Code == "READ_BINARY_DENIED" || reject.Code == "EDIT_FILE_TOO_LARGE")
}

func captureRawRevision(resolved projectpaths.Resolved, revision toolcontract.ExecutionRevision) (sourceview.Text, toolcontract.ExecutionRevision, error) {
	f, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		return sourceview.Text{}, toolcontract.ExecutionRevision{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return sourceview.Text{}, toolcontract.ExecutionRevision{}, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	revision.Mode, revision.Representation = toolcontract.PublishedDiskRevision, toolcontract.OriginalEncodedBytes
	revision.DiskSHA256, revision.ContentSHA256, revision.ContentBytes = digest, digest, size
	return sourceview.Text{}, revision, revision.Validate()
}

// captureDestinationBytes captures a destination for an exact write and
// returns its current raw bytes, which an append stages ahead of the output.
func captureDestinationBytes(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved) (sourceview.Text, toolcontract.ExecutionRevision, []byte, error) {
	st, revision, err := captureRevision(ctx, tctx, resolved, toolcontract.DestinationRevision)
	if err != nil || revision.Mode == toolcontract.AbsentRevision || st.Editor != nil {
		return st, revision, nil, err
	}
	if st.Disk != nil {
		return st, revision, st.Disk.RawBytes(), nil
	}
	f, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		return st, revision, nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil {
		return st, revision, nil, err
	}
	if textfile.SHA256(raw) != revision.DiskSHA256 {
		return st, revision, nil, &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: revision}
	}
	return st, revision, raw, nil
}
