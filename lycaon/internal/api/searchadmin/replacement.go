package searchadmin

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandlePreviewSearchReplacement(w http.ResponseWriter, r *http.Request) {
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
	compileCtx.IncludeDependencies = req.IncludeDependencies
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
		IncludeDependencies: req.IncludeDependencies,
		Query:               plan.Code.Query,
		Replacement:         req.Replacement,
		Flags:               compileCtx.Flags,
		Roots:               roots,
		ExcludeDirs:         append([]string(nil), plan.Code.LineExcludeDirs...),
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

func (s *Handler) HandleApplySearchReplacement(w http.ResponseWriter, r *http.Request) {
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
	sessionID, turn := s.chatAffiliation(r)
	response, err := s.sourceMutations.BatchWrite(r.Context(), operationID.String(), p, projectsource.SourceBatchWriteRequest{
		Input: req, SessionID: sessionID, Turn: turn,
		Prepare: func() (projectsource.SourceBatchWritePlan, error) {
			plan, planErr := search.PlanReplace(search.ReplacePlanRequest{
				Query: plan.Code.Query, Replacement: req.Replacement, Flags: compileCtx.Flags,
				Store: projectReplaceStore{project: p}, Files: files,
			})
			if planErr != nil {
				return projectsource.SourceBatchWritePlan{}, planErr
			}
			writes := make([]projectsource.SourceWriteRequest, 0, len(plan.Writes))
			for _, write := range plan.Writes {
				writes = append(writes, projectsource.SourceWriteRequest{
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
			return projectsource.SourceBatchWritePlan{Writes: writes, Response: encoded}, marshalErr
		},
	})
	if err != nil {
		if s.writeSearchQueryError(w, err) {
			return
		}
		if errors.Is(err, projectsource.ErrSourceMutationConflict) || errors.Is(err, projectsource.ErrSourceMutationDiverged) ||
			errors.Is(err, projectsource.ErrSourceWriteConflict) || errors.Is(err, projectsource.ErrSourceNotFound) ||
			errors.Is(err, projectsource.ErrSourcePathDenied) {
			s.writeSourceError(w, r, err)
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
