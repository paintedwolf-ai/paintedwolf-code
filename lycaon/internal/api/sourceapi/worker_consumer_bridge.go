package sourceapi

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// readLayeredWorkerProjectSource serves worker branch or checkpoint-scoped source reads
// through the registered consumer.Engine (#1664), propagating observation provenance,
// visibility mode, and live vs checkpoint access distinctions.
func (s *Workspace) readLayeredWorkerProjectSource(
	ctx context.Context,
	w http.ResponseWriter,
	branchRoot string,
	checkpointID string,
	p *project.Project,
	req projectsource.SourceReadRequest,
	scope sourceViewerWorkspace,
) (*wire.ProjectSourceReadResponse, bool, error) {
	if branchRoot == "" || p == nil {
		return nil, false, nil
	}
	eng, ok := requestscope.ResolveBranchConsumerEngine(branchRoot)
	if !ok || eng == nil {
		return nil, false, nil
	}
	rootID, _, err := project.ResolveWorkspaceRoot(p, req.RootID)
	if err != nil {
		return nil, true, err
	}
	relPath := strings.Trim(strings.TrimSpace(req.Path), "/")
	if relPath == "" {
		return nil, true, projectsource.ErrSourcePathInvalid
	}
	qpath := layeredfs.QualifiedPath{RootID: rootID, Rel: relPath}

	var ev consumer.EvidenceReadResult
	ckptID := strings.TrimSpace(checkpointID)
	if ckptID != "" {
		ev, err = eng.ReadCheckpointEntry(ctx, ckptID, qpath, true)
	} else {
		ev, err = eng.ReadEvidence(ctx, qpath)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, true, projectsource.ErrSourceNotFound
		}
		if errors.Is(err, fs.ErrInvalid) {
			return nil, true, projectsource.ErrSourceIsDirectory
		}
		if errors.Is(err, consumer.ErrPathEscape) || errors.Is(err, consumer.ErrDirectBackingAccessForbidden) {
			return nil, true, projectsource.ErrSourcePathDenied
		}
		return nil, true, err
	}

	if w != nil {
		w.Header().Set("X-Lycaon-Access-Source", string(ev.Source))
		w.Header().Set("X-Lycaon-Visibility-Mode", string(ev.VisibilityMode))
		w.Header().Set("X-Lycaon-Stored-In-Checkpoint", strconv.FormatBool(ev.StoredInCheckpoint))
		if ev.Observation.ObservationID != "" {
			w.Header().Set("X-Lycaon-Observation-ID", ev.Observation.ObservationID)
		}
	}

	if !utf8.Valid(ev.Content) {
		return nil, true, projectsource.ErrSourceBinary
	}
	obs := &projectsource.SourceReadObservation{
		Path:      relPath,
		RootID:    rootID,
		Revision:  projectsource.SourceRevision{SHA256: ev.SHA256, Bytes: ev.Content},
		SizeBytes: ev.Size,
		MTime:     ev.ModTime,
		MIME:      "text/plain; charset=utf-8",
		Writable:  ckptID == "",
	}
	recorded, err := s.recordedSourceObservation(ctx, p.ID, obs, scope)
	if err != nil {
		return nil, true, err
	}
	readRes := &projectsource.SourceReadResult{
		Path:      relPath,
		Content:   string(ev.Content),
		FileID:    recorded.FileID,
		VersionID: recorded.VersionID,
		SizeBytes: ev.Size,
		MTime:     ev.ModTime,
		MIME:      "text/plain; charset=utf-8",
		SHA256:    ev.SHA256,
		RootID:    rootID,
		Writable:  ckptID == "",
		Encoding:  "utf-8",
	}
	dto := ToProjectSourceReadDTO(readRes, scope.id, scope.kind)
	return &dto, true, nil
}

// appendLayeredWorkerDivergenceChanges augments worker job change listings with O(changed)
// divergence entries from a registered layered consumer.Engine (#1664).
func appendLayeredWorkerDivergenceChanges(
	ctx context.Context,
	branchRoot string,
	checkpointID string,
	files []wire.WorkerJobChangedFile,
) []wire.WorkerJobChangedFile {
	if strings.TrimSpace(branchRoot) == "" {
		return files
	}
	eng, ok := requestscope.ResolveBranchConsumerEngine(branchRoot)
	if !ok || eng == nil {
		return files
	}
	sum, err := eng.ComputeDivergence(ctx, strings.TrimSpace(checkpointID))
	if err != nil || !sum.HasDivergence {
		return files
	}
	seen := make(map[layeredfs.QualifiedPath]bool, len(files))
	for _, f := range files {
		seen[layeredfs.QualifiedPath{RootID: f.RootID, Rel: f.Path}] = true
	}
	for _, entry := range sum.Entries {
		if seen[entry.Path] || consumer.IsInternalStoragePath(entry.Path.Rel) {
			continue
		}
		seen[entry.Path] = true
		op := "modify"
		switch entry.Kind {
		case consumer.DivergenceAdded:
			op = "create"
		case consumer.DivergenceDeleted:
			op = "delete"
		case consumer.DivergenceRenamed:
			op = "move"
		}
		files = append(files, wire.WorkerJobChangedFile{
			RootID: entry.Path.RootID,
			Path:   entry.Path.Rel,
			Op:     wire.SourceChangeOp(op),
		})
	}
	return files
}
