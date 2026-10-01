package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/search"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req wire.SearchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "query is required")
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "limit must be between 1 and 500")
		return
	}
	scope, err := searchRequestScope(req)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if req.Cursor != "" {
		var response wire.SearchResponse
		generation, offset, err := globalSearchPages.DecodeAt(req.Cursor, scope, func(generation uint64) bool {
			var retained bool
			response, retained = s.searchPages.get(generation, scope)
			return retained
		})
		if err == nil && offset < 0 {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		page, err := searchResponsePage(response, generation, offset, limit, scope)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, page)
		return
	}
	svc := s.searchService()
	origin := strings.TrimSpace(req.OriginProjectID)
	if !s.searchOriginExists(w, r, origin) {
		return
	}
	budget, ok := search.ParseSearchBudget(string(req.Budget))
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "budget must be interactive or complete")
		return
	}
	compileCtx := s.searchCompileContext(r.Context(), origin)
	compileCtx.Flags = matchFlagsFromSearchRequest(req)
	compileCtx.Budget = budget
	result, err := svc.Search(r.Context(), query, compileCtx)
	if err != nil {
		s.writeSearchError(w, r, err)
		return
	}
	s.enrichSearchProjectNames(r.Context(), result)
	s.enrichSearchWorkerContext(r.Context(), result)
	response := toWireSearchResponse(result)
	generation := s.searchPages.put(scope, response)
	page, err := searchResponsePage(response, generation, 0, limit, scope)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, page)
}

// writeSearchError exposes structured query failures.
func (s *Server) writeSearchError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeSearchQueryError(w, err) {
		return
	}
	s.responses.InternalError(w, r, err)
}

// writeSearchQueryError reports whether it handled the error.
func (s *Server) writeSearchQueryError(w http.ResponseWriter, err error) bool {
	pe := &search.ParseError{}
	if errors.As(err, &pe) {
		s.responses.FailDetails(w, wire.ApiErrorCodeSearchQueryInvalid, map[string]any{
			"reason": pe.Error(),
			"offset": pe.Offset,
			"field":  pe.Field,
			"kind":   string(pe.Kind),
		}, pe.Error())
		return true
	}
	var me *search.MatchError
	if errors.As(err, &me) {
		s.responses.FailDetails(w, wire.ApiErrorCodeSearchPatternInvalid, map[string]any{
			"reason": me.Message,
		}, me.Message)
		return true
	}
	return false
}

// searchOriginExists validates the optional origin project.
func (s *Server) searchOriginExists(w http.ResponseWriter, r *http.Request, origin string) bool {
	if origin == "" {
		return true
	}
	if _, err := s.projectRegistry.Get(r.Context(), origin); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "origin project not found")
		return false
	}
	return true
}

// globalSearchPages continues a search inside the cached result generation
// that answered its first page; the position is a hit offset.
var globalSearchPages = pagecursor.For[int]("global_search")

func searchRequestScope(req wire.SearchRequest) (string, error) {
	req.Cursor = ""
	req.Limit = 0
	raw, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encode search scope: %w", err)
	}
	return string(raw), nil
}

func searchResponsePage(response wire.SearchResponse, generation uint64, offset, limit int, scope string) (wire.SearchResponse, error) {
	response.NextCursor = ""
	total := len(response.Hits)
	if offset >= total {
		response.Hits = []wire.SearchHit{}
		return response, nil
	}
	end := min(offset+limit, total)
	response.Hits = append([]wire.SearchHit(nil), response.Hits[offset:end]...)
	if end < total {
		var err error
		response.NextCursor, err = globalSearchPages.EncodeAt(scope, generation, end)
		if err != nil {
			return wire.SearchResponse{}, err
		}
	}
	return response, nil
}

func (s *Server) handleExportSearchResults(w http.ResponseWriter, r *http.Request) {
	var req wire.SearchExportRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "query is required")
		return
	}
	format := strings.ToLower(strings.TrimSpace(string(req.Format)))
	if !isSearchExportFormat(format) {
		s.responses.Fail(w, wire.ApiErrorCodeSearchExportInvalidFormat, "format must be jsonl, csv, or sarif")
		return
	}
	svc := s.searchService()
	origin := strings.TrimSpace(req.OriginProjectID)
	if !s.searchOriginExists(w, r, origin) {
		return
	}
	compileCtx := s.searchCompileContext(r.Context(), origin)
	compileCtx.Flags = matchFlagsFromSearchExport(req)
	outcome, err := svc.Export(r.Context(), query, compileCtx)
	if err != nil {
		s.writeSearchError(w, r, err)
		return
	}
	if format == search.ExportFormatSARIF {
		if !search.PlanSARIFScoped(outcome.Plan) || !search.HitsSARIFEligible(outcome.Result.Hits) {
			s.responses.Fail(w, wire.ApiErrorCodeSearchExportSarifScope,
				"SARIF export needs a findings-only query — add kind:scan",
			)
			return
		}
	}
	s.enrichSearchProjectNames(r.Context(), outcome.Result)
	s.enrichSearchWorkerContext(r.Context(), outcome.Result)
	w.Header().Set("Content-Type", search.ExportContentType(format))
	w.Header().Set("X-Export-Truncated", strconv.FormatBool(outcome.Truncated))
	filename := search.ExportFilename(format, string(outcome.Plan.Interpretation.Scope), time.Now().UTC().Format(time.RFC3339))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	switch format {
	case search.ExportFormatSARIF:
		data, err := scanoutput.ExportSARIF(searchHitsToSecurityFindings(outcome.Result.Hits), outcome.Truncated)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if _, err := w.Write(data); err != nil {
			s.responses.InternalError(w, r, err)
		}
	default:
		if err := search.WriteExport(w, format, outcome.Result.Hits, outcome.Truncated); err != nil {
			s.responses.InternalError(w, r, err)
		}
	}
}

func searchHitsToSecurityFindings(hits []search.Hit) []wire.SecurityFinding {
	out := make([]wire.SecurityFinding, 0, len(hits))
	for _, hit := range hits {
		ruleID := strings.TrimSpace(hit.Handle)
		if ruleID == "" {
			ruleID = strings.TrimSpace(hit.HintCode)
		}
		uri := strings.TrimSpace(hit.Path)
		if uri == "" {
			uri = strings.TrimSpace(hit.SourceRef)
		}
		level := wire.FindingLevelInfo
		switch strings.ToLower(strings.TrimSpace(hit.Trust)) {
		case "critical":
			level = wire.FindingLevelCritical
		case "error", "high":
			level = wire.FindingLevelHigh
		case "warning", "medium":
			level = wire.FindingLevelMedium
		case "low":
			level = wire.FindingLevelLow
		}
		out = append(out, scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID: "search-export",
			RuleID:   ruleID,
			Level:    level,
			Message:  hit.Snippet,
			Kind:     wire.FindingKindCustom,
			Locations: []wire.SecurityFindingLocation{{
				URI:       uri,
				StartLine: hit.Line,
			}},
		}))
	}
	return out
}

func isSearchExportFormat(format string) bool {
	for _, allowed := range search.ExportFormats() {
		if format == allowed {
			return true
		}
	}
	return false
}

func (s *Server) searchCompileContext(ctx context.Context, originProjectID string) search.CompileContext {
	return search.CompileContext{
		OriginProjectID:        originProjectID,
		ResolveProjectBySlug:   s.resolveSearchProjectSlug(ctx),
		RootsForProject:        s.rootsForSearchProject(ctx),
		AttachedProjectIDs:     s.attachedSearchProjectIDs(ctx),
		DependencyPathPatterns: sourceapi.SearchDependencyPatterns(),
	}
}

func (s *Server) resolveSearchProjectSlug(ctx context.Context) func(slug string) (string, error) {
	return func(slug string) (string, error) {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			return "", fmt.Errorf("empty slug")
		}
		projects, err := s.projectRegistry.List(ctx)
		if err != nil {
			return "", err
		}
		for _, p := range projects {
			if searchProjectSlug(p) == slug {
				return p.ID, nil
			}
			for _, root := range p.Roots {
				if strings.EqualFold(root.Label, slug) {
					return p.ID, nil
				}
			}
			if strings.EqualFold(strings.TrimSpace(p.Name), slug) {
				return p.ID, nil
			}
		}
		return "", fmt.Errorf("unknown project slug")
	}
}

func (s *Server) rootsForSearchProject(ctx context.Context) func(projectID string) ([]search.CodeRoot, error) {
	return func(projectID string) ([]search.CodeRoot, error) {
		p, err := s.projectRegistry.Get(ctx, strings.TrimSpace(projectID))
		if err != nil {
			return nil, err
		}
		var roots []search.CodeRoot
		for _, root := range p.Roots {
			if path := strings.TrimSpace(root.Path); path != "" {
				roots = append(roots, search.CodeRoot{RootID: root.ID, Path: path})
			}
		}
		return roots, nil
	}
}

func (s *Server) attachedSearchProjectIDs(ctx context.Context) func() ([]string, error) {
	return func() ([]string, error) {
		projects, err := s.projectRegistry.List(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(projects))
		for _, p := range projects {
			if strings.TrimSpace(p.ID) != "" {
				ids = append(ids, p.ID)
			}
		}
		return ids, nil
	}
}

func (s *Server) enrichSearchProjectNames(ctx context.Context, result *search.Result) {
	if result == nil {
		return
	}
	nameByID := map[string]string{}
	for i := range result.Hits {
		id := strings.TrimSpace(result.Hits[i].ProjectID)
		if id == "" {
			continue
		}
		name, ok := nameByID[id]
		if !ok {
			p, err := s.projectRegistry.Get(ctx, id)
			if err != nil {
				continue
			}
			name = searchProjectDisplayName(p)
			nameByID[id] = name
		}
		result.Hits[i].ProjectName = name
	}
}

// enrichSearchWorkerContext resolves child-session navigation coordinates.
func (s *Server) enrichSearchWorkerContext(ctx context.Context, result *search.Result) {
	if result == nil || len(result.Hits) == 0 {
		return
	}
	type workerRef struct {
		parentSessionID string
		workerID        string
	}
	resolved := map[string]workerRef{}
	for i := range result.Hits {
		childSessionID := strings.TrimSpace(result.Hits[i].SessionID)
		if childSessionID == "" {
			continue
		}
		ref, seen := resolved[childSessionID]
		if !seen {
			var parent, worker string
			err := s.database.QueryRowContext(ctx,
				`SELECT parent_session_id, id FROM worker_jobs WHERE child_session_id = ? LIMIT 1`,
				childSessionID,
			).Scan(&parent, &worker)
			if err == nil {
				ref = workerRef{parentSessionID: strings.TrimSpace(parent), workerID: strings.TrimSpace(worker)}
			}
			resolved[childSessionID] = ref
		}
		result.Hits[i].ParentSessionID = ref.parentSessionID
		result.Hits[i].WorkerID = ref.workerID
	}
}

func searchProjectSlug(p project.Project) string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return project.SlugProjectName(name)
	}
	for _, root := range p.Roots {
		if root.IsPrimary {
			return root.Label
		}
	}
	return ""
}

func searchProjectDisplayName(p *project.Project) string {
	if p == nil {
		return ""
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	for _, root := range p.Roots {
		if root.IsPrimary {
			return root.Label
		}
	}
	return p.ID
}

// searchService reads the evidence index through the host database.
func (s *Server) searchService() *search.Service {
	return search.NewService(s.database, s.rerank, sourceapi.NewSymbolExecutor(s.projectRegistry))
}

func toWireSearchResponse(result *search.Result) wire.SearchResponse {
	if result == nil {
		return wire.SearchResponse{}
	}
	out := wire.SearchResponse{
		Hits:             make([]wire.SearchHit, 0, len(result.Hits)),
		Status:           wire.SearchResultStatus(result.Status),
		Exhaustive:       result.Exhaustive,
		TotalHits:        len(result.Hits),
		CountRelation:    wire.SearchCountRelation(result.CountRelation),
		FacetsExhaustive: result.Exhaustive,
		Facets:           make([]wire.SearchFacet, 0, len(result.Facets)),
		Histogram:        make([]wire.SearchHistogramBucket, 0, len(result.Histogram)),
		Issues:           make([]wire.SearchIssue, 0, len(result.Issues)),
		Interpreted: wire.SearchInterpretation{
			Scope:                   wire.SearchScopeMode(result.Interpretation.Scope),
			Slug:                    result.Interpretation.Slug,
			FTSTerms:                append([]string(nil), result.Interpretation.FTSTerms...),
			Filters:                 make([]wire.SearchInterpretedFilter, 0, len(result.Interpretation.Filters)),
			DependencyTreesExcluded: result.Interpretation.DependencyTreesExcluded,
		},
	}
	for _, f := range result.Interpretation.Filters {
		out.Interpreted.Filters = append(out.Interpreted.Filters, wire.SearchInterpretedFilter{
			Field:   f.Field,
			Value:   f.Value,
			Negated: f.Negated,
		})
	}
	for _, hit := range result.Hits {
		var highlights []wire.SourceSearchHighlight
		for _, span := range hit.TitleHighlights {
			highlights = append(highlights, wire.SourceSearchHighlight{Start: span.Start, End: span.End})
		}
		var createdAt time.Time
		if hit.TS != "" {
			if t, err := time.Parse(time.RFC3339Nano, hit.TS); err == nil {
				createdAt = t
			}
		}
		out.Hits = append(out.Hits, wire.SearchHit{
			HitID:           hit.ID,
			HitKind:         hit.HitKind,
			Source:          hit.Source,
			Score:           hit.Score,
			SessionID:       hit.SessionID,
			SourceRef:       hit.SourceRef,
			LegID:           hit.LegID,
			Handle:          hit.Handle,
			ParentSessionID: hit.ParentSessionID,
			WorkerID:        hit.WorkerID,
			ProjectID:       hit.ProjectID,
			RootID:          hit.RootID,
			ProjectName:     hit.ProjectName,
			Snippet:         hit.Snippet,
			Title:           hit.Title,
			Context:         hit.Context,
			Path:            hit.Path,
			Line:            hit.Line,
			URL:             hit.URL,
			Trust:           hit.Trust,
			Verified:        hit.Verified,
			HintCode:        hit.HintCode,
			SymbolKind:      wire.SourceSymbolKind(hit.SymbolKind),
			TitleHighlights: highlights,
			CreatedAt:       createdAt,
		})
	}
	for _, facet := range result.Facets {
		wf := wire.SearchFacet{
			Key:    facet.Key,
			Values: make([]wire.SearchFacetValue, 0, len(facet.Values)),
		}
		for _, value := range facet.Values {
			wf.Values = append(wf.Values, wire.SearchFacetValue{
				Value: value.Value,
				Count: value.Count,
			})
		}
		out.Facets = append(out.Facets, wf)
	}
	for _, bucket := range result.Histogram {
		out.Histogram = append(out.Histogram, wire.SearchHistogramBucket{
			Key:   bucket.Key,
			Count: bucket.Count,
		})
	}
	out.Issues = searchIssuesWire(result.Issues)
	return out
}

func searchIssuesWire(issues []search.Issue) []wire.SearchIssue {
	out := make([]wire.SearchIssue, 0, len(issues))
	for _, issue := range issues {
		out = append(out, wire.SearchIssue{Executor: issue.Executor, Reason: wire.SearchIssueReason(issue.Reason),
			Limit: issue.Limit, Count: issue.Count, Message: issue.Message})
	}
	return out
}

func matchFlagsFromSearchRequest(req wire.SearchRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}

func matchFlagsFromSearchExport(req wire.SearchExportRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}

func matchFlagsFromReplacePreview(req wire.SearchReplacePreviewRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}

func matchFlagsFromReplaceApply(req wire.SearchReplaceApplyRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}

func (s *Server) handlePreviewSearchReplacement(w http.ResponseWriter, r *http.Request) {
	var req wire.SearchReplacePreviewRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "query is required")
		return
	}
	origin := strings.TrimSpace(req.OriginProjectID)
	compileCtx := s.searchCompileContext(r.Context(), origin)
	compileCtx.Flags = matchFlagsFromReplacePreview(req)
	plan, err := search.CompileQuery(query, compileCtx)
	if err != nil {
		s.writeSearchError(w, r, err)
		return
	}
	if plan.Code == nil || !plan.Code.Lines {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "replacement requires a content search query")
		return
	}
	roots := replaceRoots(origin, plan.Code.PathRoots)
	result, err := search.PreviewReplace(r.Context(), search.ReplacePreviewRequest{
		Query:       plan.Code.Query,
		Replacement: req.Replacement,
		Flags:       compileCtx.Flags,
		Roots:       roots,
		ExcludeDirs: append([]string(nil), plan.Code.LineExcludeDirs...),
	})
	if err != nil {
		s.writeSearchError(w, r, err)
		return
	}
	files := make([]wire.SearchReplaceFilePreview, 0, len(result.Files))
	for _, f := range result.Files {
		hunks := make([]wire.SearchReplaceHunk, 0, len(f.Hunks))
		for _, h := range f.Hunks {
			hunks = append(hunks, wire.SearchReplaceHunk{
				Line: h.Line, EndLine: h.EndLine,
				Before: h.Before, After: h.After,
				Context: h.Context,
			})
		}
		files = append(files, wire.SearchReplaceFilePreview{
			RootID: f.RootID, Path: f.Path, SHA256: f.SHA256, Hunks: hunks,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SearchReplacePreviewResponse{
		State: wire.SearchReplacePreviewState(result.State), Files: files, Truncated: result.Truncated, Issues: searchIssuesWire(result.Issues),
	})
}

func (s *Server) handleApplySearchReplacement(w http.ResponseWriter, r *http.Request) {
	var req wire.SearchReplaceApplyRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "query is required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	origin := strings.TrimSpace(req.OriginProjectID)
	if origin == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "origin_project_id is required")
		return
	}
	p, err := s.projectRegistry.Get(r.Context(), origin)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
		return
	}
	compileCtx := s.searchCompileContext(r.Context(), origin)
	compileCtx.Flags = matchFlagsFromReplaceApply(req)
	plan, err := search.CompileQuery(query, compileCtx)
	if err != nil {
		s.writeSearchError(w, r, err)
		return
	}
	if plan.Code == nil || !plan.Code.Lines {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "replacement requires a content search query")
		return
	}
	if plan.Scope != search.ScopeGlobal && !slices.ContainsFunc(plan.Code.PathRoots, func(root search.CodeRoot) bool {
		return root.ProjectID == origin
	}) {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "replacement query must target the origin project")
		return
	}
	files := make([]search.ReplaceApplyFile, 0, len(req.Files))
	for _, f := range req.Files {
		files = append(files, search.ReplaceApplyFile{
			RootID: f.RootID, Path: f.Path, SHA256: f.SHA256, HunkIndexes: f.Hunks,
		})
	}
	sessionID, turn := s.Sources.UserSourceChatAffiliation(r)
	response, err := s.Sources.SourceMutations.BatchWrite(r.Context(), operationID.String(), p, project.SourceBatchWriteRequest{
		Input: req, SessionID: sessionID, Turn: turn,
		Prepare: func() (project.SourceBatchWritePlan, error) {
			plan, planErr := search.PlanReplace(search.ReplacePlanRequest{
				Query: plan.Code.Query, Replacement: req.Replacement, Flags: compileCtx.Flags,
				Store: projectReplaceStore{project: p}, Files: files,
			})
			if planErr != nil {
				return project.SourceBatchWritePlan{}, planErr
			}
			writes := make([]project.SourceWriteRequest, 0, len(plan.Writes))
			for _, write := range plan.Writes {
				writes = append(writes, project.SourceWriteRequest{
					RootID: write.RootID, Path: write.Path, Content: write.Content,
					Encoding: write.Encoding, BaseSHA256: write.BaseSHA256,
				})
			}
			out := make([]wire.SearchReplaceFileOutcome, 0, len(plan.Files))
			for _, file := range plan.Files {
				out = append(out, wire.SearchReplaceFileOutcome{
					RootID: file.RootID, Path: file.Path, Applied: file.Applied, Skipped: file.Skipped,
					Reason: file.Reason, Matches: file.Matches,
				})
			}
			encoded, marshalErr := json.Marshal(wire.SearchReplaceApplyResponse{Files: out, BatchID: operationID.String()})
			return project.SourceBatchWritePlan{Writes: writes, Response: encoded}, marshalErr
		},
	})
	if err != nil {
		if s.writeSearchQueryError(w, err) {
			return
		}
		if errors.Is(err, project.ErrSourceMutationConflict) || errors.Is(err, project.ErrSourceMutationDiverged) ||
			errors.Is(err, project.ErrSourceWriteConflict) || errors.Is(err, project.ErrSourceNotFound) ||
			errors.Is(err, project.ErrSourcePathDenied) {
			s.Sources.WriteProjectSourceError(w, r, err)
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	var result wire.SearchReplaceApplyResponse
	if err := json.Unmarshal(response, &result); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}

func replaceRoots(originProjectID string, planRoots []search.CodeRoot) []search.CodeRoot {
	origin := strings.TrimSpace(originProjectID)
	return slices.DeleteFunc(slices.Clone(planRoots), func(root search.CodeRoot) bool {
		return origin != "" && root.ProjectID != origin
	})
}
