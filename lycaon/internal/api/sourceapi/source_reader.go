package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func readerEndpoint(side wire.SourceComparisonSide) *wire.SourceReaderEndpoint {
	var screen *wire.SecretScreen
	if side.SecretScreen != nil {
		value := *side.SecretScreen
		value.Spans = nil
		screen = &value
	}
	return &wire.SourceReaderEndpoint{VersionID: side.VersionID, RootID: side.RootID, Path: side.Path, State: side.State, Sha256: side.Sha256, SizeBytes: side.SizeBytes, Availability: side.Availability, Reason: side.Reason, SecretScreen: screen}
}

func readerTextSide(path, text string) wire.SourceComparisonSide {
	return wire.SourceComparisonSide{Path: path, Content: text, State: "content", Availability: "available", Sha256: sourcecomparison.Hash(text), SizeBytes: int64(len(text))}
}

func (s *Comparisons) readChatFileEdit(ctx context.Context, sessionID string, ref wire.FileEditReference) (wire.FileEditSnapshot, error) {
	msg, err := s.SessionStore.GetMessage(ctx, sessionID, ref.MessageID)
	if err != nil {
		return wire.FileEditSnapshot{}, err
	}
	msg = messageview.RedactMessage(msg)
	if result := msg.ToolResult; result != nil && result.ToolCallID == ref.ToolCallID {
		if ref.Index == 0 && result.FileEdit != nil {
			return *result.FileEdit, nil
		}
		if ref.Index > 0 && result.OverlayPromotion != nil && ref.Index <= len(result.OverlayPromotion.Files) {
			return result.OverlayPromotion.Files[ref.Index-1], nil
		}
	}
	return wire.FileEditSnapshot{}, errors.New("file edit not found")
}

func (s *Comparisons) readerCurrentSide(ctx context.Context, p *project.Project, rootID, path string) (wire.SourceComparisonSide, error) {
	snapshot, err := s.EditorDocuments.ResolveSourceSnapshot(ctx, p, projectsource.SourceReadRequest{RootID: rootID, Path: path}, editordoc.ObserveCurrent)
	if errors.Is(err, projectsource.ErrSourceNotFound) {
		return wire.SourceComparisonSide{Path: path, State: "absent", Availability: "absent"}, nil
	}
	if err != nil {
		return wire.SourceComparisonSide{}, err
	}
	if snapshot.Document != nil {
		return readerTextSide(path, snapshot.Document.Draft), nil
	}
	read := snapshot.Source
	if read.Binary || read.OverLimit {
		return wire.SourceComparisonSide{Path: path, State: "content", Availability: "unavailable"}, nil
	}
	return readerTextSide(path, read.Content), nil
}

func (s *Comparisons) writeSourceComparison(w http.ResponseWriter, r *http.Request, projectID string, comparison sourceledger.Comparison) {
	httpio.WriteJSON(w, http.StatusOK, s.MapSourceComparison(r.Context(), projectID, comparison))
}

func (s *Comparisons) HandleGetProjectSourceComparison(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	effectID := strings.TrimSpace(q.Get("effect_id"))
	versionID := strings.TrimSpace(q.Get("version_id"))
	fileID := strings.TrimSpace(q.Get("file_id"))
	blobOID := strings.TrimSpace(q.Get("blob_oid"))
	path := q.Get("path")
	modeCount := 0
	for _, value := range []string{effectID, versionID, fileID, blobOID, path} {
		if value != "" {
			modeCount++
		}
	}
	if modeCount != 1 {
		s.responses.InvalidQueryParam(w, "effect_id",
			"name exactly one comparison: effect_id, version_id, blob_oid, file_id, or root_id and path")
		return
	}
	if (q.Has("mark_user_edits") || q.Has("presentation_after_ordinal")) && fileID == "" {
		param := "presentation_after_ordinal"
		if q.Has("mark_user_edits") {
			param = "mark_user_edits"
		}
		s.responses.InvalidQueryParam(w, param, "applies only to a recorded scope comparison by file_id")
		return
	}
	if q.Has("reviewed_through_ordinal") {
		s.writeSourceReviewedComparison(w, r, p, fileID)
		return
	}
	if path != "" {
		s.Review.writeCommitPathComparison(w, r, p)
		return
	}
	if effectID != "" {
		s.writeSourceEffectComparison(w, r, p, effectID)
		return
	}
	if versionID != "" {
		s.writeSourceVersionComparison(w, r, p, versionID)
		return
	}
	if blobOID != "" {
		s.Review.writeSourceBlobComparison(w, r, p,
			strings.TrimSpace(q.Get("root_id")), blobOID,
			strings.TrimSpace(q.Get("before_blob_oid")))
		return
	}
	s.writeSourceScopeDiff(w, r, p, fileID)
}

func (s *Comparisons) writeSourceScopeDiff(w http.ResponseWriter, r *http.Request, p *project.Project, fileID string) {
	bas, err := sourceledger.ParseBaseline(r.URL.Query().Get("baseline"))
	if err != nil {
		s.responses.InvalidQuery(w, &httpio.QueryParameterError{Parameter: "baseline", Reason: "is not a source baseline"})
		return
	}
	if bas.Kind == sourceledger.BaselineCommit {
		s.responses.InvalidQueryParam(w, "baseline", "commit requires root_id and path")
		return
	}
	start, err := presentationComparisonStart(r, bas)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	mark, present, err := httpio.OptionalBoolQuery(r, "mark_user_edits")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	var markUserEdits *bool
	if present {
		markUserEdits = &mark
	}
	diff, err := s.loadScopeComparison(r.Context(), p, wire.ScopeComparisonSource{
		FileID: fileID, Baseline: r.URL.Query().Get("baseline"),
		MarkUserEdits: markUserEdits, PresentationAfterOrdinal: start,
	})
	if err != nil {
		s.writeComparisonError(w, r, err)
		return
	}
	s.writeSourceComparison(w, r, p.ID, diff)
}

func (s *Comparisons) writeSourceEffectComparison(w http.ResponseWriter, r *http.Request, p *project.Project, effectID string) {
	diff, err := s.SourceLedger.CompareEffect(r.Context(), p.ID, effectID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceEffectNotFound, "effect not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.writeSourceComparison(w, r, p.ID, diff)
}

func (s *Comparisons) writeSourceVersionComparison(w http.ResponseWriter, r *http.Request, p *project.Project, versionID string) {
	diff, err := s.SourceLedger.CompareVersions(r.Context(), p.ID, versionID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSourceVersionNotFound, "version not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.writeSourceComparison(w, r, p.ID, diff)
}

func (s *Comparisons) MapSourceComparison(ctx context.Context, projectID string, d sourceledger.Comparison) wire.SourceComparison {
	out := mapUnscreenedSourceComparison(d)
	if d.InRange {
		screenCtx, _, err := secretview.ProjectContext(s.ManagedSecrets, ctx, projectID)
		if err == nil {
			for _, side := range []*wire.SourceComparisonSide{out.Before, out.After} {
				if side.Availability == "available" {
					side.SecretScreen = secretview.ScreenText(s.SecretSpans, screenCtx, side.Content)
				}
			}
		}
	}
	return out
}

func mapUnscreenedSourceComparison(d sourceledger.Comparison) wire.SourceComparison {
	out := wire.SourceComparison{
		PresentationAfterOrdinal: d.PresentationAfterOrdinal,
		InRange:                  d.InRange, EffectID: d.EffectID, FileID: d.FileID, Op: d.Op,
		LocationChanged: d.LocationChanged, UserEditsUnmarked: d.UserEditsUnmarked, Attribution: mapSourceAttribution(d.Attribution),
	}
	// Out-of-range comparisons omit both endpoints.
	if d.InRange {
		before, after := mapSourceComparisonSide(d.Before), mapSourceComparisonSide(d.After)
		out.Before, out.After = &before, &after
	}
	return out
}

func mapSourceComparisonSide(side sourceledger.ComparisonSide) wire.SourceComparisonSide {
	return wire.SourceComparisonSide{VersionID: side.VersionID, RootID: side.RootID,
		Path: side.Path, State: side.State, Sha256: side.SHA256, SizeBytes: side.SizeBytes,
		Availability: string(side.Availability), Reason: side.Reason, Content: side.Content}
}

// commitLens binds attached roots to known repository tops.
func (s *Comparisons) CommitLens(ctx context.Context, p *project.Project) sourceledger.CommitLens {
	mgr := s.Git.Manager()
	roots := project.RootRefsFrom(p)
	repos, err := s.Git.LoadOrderedRepos(ctx, p, roots, "", "")
	if err != nil {
		return sourceledger.CommitLens{}
	}
	topByRootID := make(map[string]string, len(roots))
	for _, repo := range repos {
		if !repo.Available || strings.TrimSpace(repo.Toplevel) == "" {
			continue
		}
		for _, id := range repo.RootIDs {
			topByRootID[id] = repo.Toplevel
		}
	}
	lens := sourceledger.CommitLens{}
	tops := make(map[string]string, len(roots))
	for _, root := range roots {
		top, inRepo := topByRootID[root.ID]
		if !inRepo {
			continue
		}
		abs, err := filepath.Abs(root.Path)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		tops[abs] = top
		lens.Roots = append(lens.Roots, sourceledger.LensRoot{ID: root.ID, Abs: abs})
	}
	lens.Available = len(lens.Roots) > 0
	lens.Git = gitWorkingTreeAdapter{mgr: mgr, tops: tops}
	return lens
}
