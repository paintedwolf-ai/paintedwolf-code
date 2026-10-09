package briefingadmin

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type resolvedFileBriefing struct {
	input  filebriefing.Input
	rootID string
	path   string
}

type fileBriefingValidationError struct{ message string }

func (e fileBriefingValidationError) Error() string { return e.message }

func invalidFileBriefingRequest(message string) error {
	return fileBriefingValidationError{message: message}
}

func (s *Handler) resolveFileBriefing(ctx context.Context, p *project.Project, req wire.FileBriefingRequest) (resolvedFileBriefing, error) {
	req.RootID, req.Path = strings.TrimSpace(req.RootID), strings.TrimSpace(req.Path)
	req.Presentation = strings.TrimSpace(req.Presentation)
	req.WorkerID, req.DocumentID = strings.TrimSpace(req.WorkerID), strings.TrimSpace(req.DocumentID)
	req.VersionID = strings.TrimSpace(req.VersionID)
	if req.RootID == "" || req.Path == "" {
		return resolvedFileBriefing{}, invalidFileBriefingRequest("root_id and path are required")
	}
	var resolved resolvedFileBriefing
	var err error
	switch req.Presentation {
	case "current":
		if req.DocumentID != "" || req.DocumentRevision != 0 || req.VersionID != "" {
			return resolvedFileBriefing{}, invalidFileBriefingRequest("current presentation accepts only worker_id")
		}
		resolved, err = s.resolveCurrentBriefing(ctx, p, req)
	case "document":
		if req.WorkerID != "" || req.VersionID != "" {
			return resolvedFileBriefing{}, invalidFileBriefingRequest("document presentation accepts only document_id and document_revision")
		}
		resolved, err = s.resolveDocumentBriefing(ctx, p, req)
	case "version":
		if req.WorkerID != "" || req.DocumentID != "" || req.DocumentRevision != 0 {
			return resolvedFileBriefing{}, invalidFileBriefingRequest("version presentation accepts only version_id")
		}
		resolved, err = s.resolveVersionBriefing(ctx, p, req)
	default:
		return resolvedFileBriefing{}, invalidFileBriefingRequest("presentation must be current, document, or version")
	}
	if err != nil {
		return resolvedFileBriefing{}, err
	}
	return resolved, nil
}

func (s *Handler) resolveVersionBriefing(ctx context.Context, p *project.Project, req wire.FileBriefingRequest) (resolvedFileBriefing, error) {
	if req.VersionID == "" {
		return resolvedFileBriefing{}, invalidFileBriefingRequest("version presentation requires version_id")
	}
	version, err := s.SourceLedger.ReadRestorableVersion(ctx, p.ID, req.VersionID)
	if err != nil {
		return resolvedFileBriefing{}, err
	}
	if version.RootID != req.RootID || version.Path != req.Path {
		return resolvedFileBriefing{}, invalidFileBriefingRequest("version does not match root_id and path")
	}
	if version.State != "content" || version.SHA256 == "" {
		return resolvedFileBriefing{}, sourceledger.ErrVersionUnavailable
	}
	if len(version.Content) > project.SourceReadMaxBytes {
		return resolvedFileBriefing{}, project.ErrSourceBinary
	}
	text, ok := version.Text()
	if !ok {
		return resolvedFileBriefing{}, project.ErrSourceBinary
	}
	return resolvedFileBriefing{rootID: version.RootID, path: version.Path, input: filebriefing.Input{
		Path: version.Path, Presentation: req.Presentation, Source: text, SourceSHA256: version.SHA256,
	}}, nil
}

func (s *Handler) resolveCurrentBriefing(ctx context.Context, p *project.Project, req wire.FileBriefingRequest) (resolvedFileBriefing, error) {
	branchRoot, releaseBranch := s.workerBranchRoot(ctx, p.ID, req.WorkerID)
	defer releaseBranch()
	p, err := requestscope.SourceProjectInBranch(p, branchRoot)
	if err != nil {
		return resolvedFileBriefing{}, err
	}
	read, err := project.ReadProjectSource(p, project.SourceReadRequest{
		Path: req.Path, RootID: req.RootID,
	})
	if err != nil {
		return resolvedFileBriefing{}, err
	}
	if read.Binary || read.OverLimit || strings.TrimSpace(read.SHA256) == "" {
		return resolvedFileBriefing{}, project.ErrSourceBinary
	}
	return resolvedFileBriefing{rootID: read.RootID, path: read.Path, input: filebriefing.Input{
		Path: read.Path, Presentation: req.Presentation, Source: read.Content, SourceSHA256: read.SHA256,
	}}, nil
}

func (s *Handler) resolveDocumentBriefing(ctx context.Context, p *project.Project, req wire.FileBriefingRequest) (resolvedFileBriefing, error) {
	if strings.TrimSpace(req.DocumentID) == "" || req.DocumentRevision < 1 {
		return resolvedFileBriefing{}, invalidFileBriefingRequest("document presentation requires document_id and document_revision")
	}
	document, err := s.EditorDocuments.Snapshot(ctx, p.ID, req.DocumentID, int64(req.DocumentRevision))
	if err != nil {
		return resolvedFileBriefing{}, err
	}
	if document.RootID != req.RootID || document.Path != req.Path {
		return resolvedFileBriefing{}, invalidFileBriefingRequest("document does not match root_id and path")
	}
	return resolvedFileBriefing{rootID: document.RootID, path: document.Path, input: filebriefing.Input{
		Path: document.Path, Presentation: req.Presentation, Source: document.Draft,
		SourceSHA256: sourceblob.ContentSHA([]byte(document.Draft)),
	}}, nil
}

func FileBriefingRequestError(err error) (wire.ApiErrorCode, string, bool) {
	var validation fileBriefingValidationError
	switch {
	case errors.As(err, &validation):
		return wire.ApiErrorCodeInvalidRequest, validation.message, true
	case errors.Is(err, editordoc.ErrRevisionConflict):
		return wire.ApiErrorCodeEditorRevisionConflict, "editor revision changed", true
	case errors.Is(err, editordoc.ErrNotFound):
		return wire.ApiErrorCodeEditorDocumentNotFound, "briefing source not found", true
	case errors.Is(err, sourceledger.ErrHistoryNotFound):
		return wire.ApiErrorCodeSourceVersionNotFound, "briefing source version not found", true
	case errors.Is(err, sourceledger.ErrVersionUnavailable):
		return wire.ApiErrorCodeSourceVersionUnavailable, "exact content for this version is unavailable", true
	case errors.Is(err, project.ErrSourceBinary):
		return wire.ApiErrorCodeSourceBinary, "file briefing requires a readable text presentation", true
	case errors.Is(err, project.ErrSourceUnsupportedEncoding):
		return wire.ApiErrorCodeUnsupportedEncoding, "file briefing requires a readable text presentation", true
	case errors.Is(err, project.ErrSourceNotFound):
		return wire.ApiErrorCodeSourceNotFound, "briefing source not found", true
	case errors.Is(err, project.ErrSourcePathAmbiguous):
		return wire.ApiErrorCodeSourcePathAmbiguous, "briefing source path is ambiguous", true
	case errors.Is(err, project.ErrSourcePathInvalid):
		return wire.ApiErrorCodeInvalidPath, "briefing source path is invalid", true
	case errors.Is(err, project.ErrSourcePathDenied):
		return wire.ApiErrorCodeSourcePathDenied, "briefing source is outside the project", true
	case errors.Is(err, project.ErrSourceNoRoot):
		return wire.ApiErrorCodeNoProjectRoot, "project has no attached folder", true
	default:
		return "", "", false
	}
}

func (r resolvedFileBriefing) target(p *project.Project) filebriefing.Target {
	return filebriefing.Target{ProjectID: p.ID, RootID: r.rootID, ProjectDir: projectRootPath(p, r.rootID), Input: r.input}
}
