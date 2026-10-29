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
// native utilities read and publish through. A capture serves the accepted
// document when one exists and published disk otherwise. It never consults or
// advances the conversational read bases that structured edits require.
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
// after capture. The prepared change is stopped, never refreshed or rerun.
// Current is empty when the owner refused the publication itself.
type ExecutionRevisionConflict struct {
	Path    string
	Pinned  toolcontract.ExecutionRevision
	Current toolcontract.ExecutionRevision
	Cause   error
}

func (c *ExecutionRevisionConflict) Error() string {
	return fmt.Sprintf("program %s %s changed since it was captured", c.Pinned.Role, c.Path)
}

func (c *ExecutionRevisionConflict) Unwrap() []error {
	reject := &toolrejection.ToolReject{Code: "COMMAND_REVISION_CONFLICT", Data: map[string]any{"path": c.Path}}
	if c.Cause == nil {
		return []error{reject}
	}
	return []error{reject, c.Cause}
}

// CaptureSource pins one native input before the transform that reads it.
func (e ExecutionRevisions) CaptureSource(ctx context.Context, tctx tools.ToolContext, path string) (ExecutionSource, error) {
	resolved, err := projectpaths.ResolveRead(ctx, e.Boundary, tctx, path)
	if err != nil {
		return ExecutionSource{}, err
	}
	st, revision, err := captureRevision(ctx, tctx, resolved, toolcontract.SourceRevision)
	if err != nil {
		return ExecutionSource{}, err
	}
	if revision.Mode == toolcontract.AbsentRevision {
		return ExecutionSource{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
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
		_, current, err := captureRevision(ctx, tctx, resolved, pin.Role)
		if errors.Is(err, fs.ErrNotExist) || toolrejection.AsToolReject(err) != nil {
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

func captureRevision(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, role toolcontract.ExecutionRevisionRole) (sourceview.Text, toolcontract.ExecutionRevision, error) {
	root, rel := revisionLocation(resolved)
	revision := toolcontract.ExecutionRevision{Role: role, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel}
	tool, access := "read", sourceview.AccessRead
	if role == toolcontract.DestinationRevision {
		tool, access = "write", sourceview.AccessMutate
	}
	st, err := sourceview.LoadText(ctx, tool, access, tctx, resolved)
	if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
		if role == toolcontract.SourceRevision {
			return sourceview.Text{}, toolcontract.ExecutionRevision{}, err
		}
		revision.Mode = toolcontract.AbsentRevision
		return sourceview.Text{}, revision, nil
	}
	if err != nil {
		return sourceview.Text{}, toolcontract.ExecutionRevision{}, err
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
