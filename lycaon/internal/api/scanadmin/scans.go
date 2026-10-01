package scanadmin

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleListCodeScans(w http.ResponseWriter, r *http.Request) {
	_, canonicalPaths, ok := s.requireProjectScanPaths(w, r)
	if !ok {
		return
	}
	limit := 0
	if n, present, err := httpio.OptionalIntQuery(r, "limit", 1, 100); err != nil {
		s.responses.InvalidQuery(w, err)
		return
	} else if present {
		limit = n
	}
	out, err := s.Coordinator.ListPage(r.Context(), canonicalPaths, scan.PageQuery{Limit: limit, Cursor: r.URL.Query().Get("cursor"), Sort: r.URL.Query().Get("sort"), Order: r.URL.Query().Get("order")})
	var queryErr *scan.PageQueryError
	switch {
	case errors.As(err, &queryErr):
		s.responses.InvalidQueryParam(w, queryErr.Param, "is not a supported value")
		return
	case errors.Is(err, scan.ErrInvalidScanPageCursor):
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	case err != nil:
		s.responses.InternalError(w, r, err)
		return
	}
	if out.Scans == nil {
		out.Scans = []wire.CodeScan{}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) HandleGetCodeScan(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireProjectScanID(w, r)
	if !ok {
		return
	}
	view, err := scan.ParseScanView(r.URL.Query().Get("view"))
	if err != nil {
		s.responses.InvalidQuery(w, &httpio.QueryParameterError{Parameter: "view", Reason: "must be summary or full"})
		return
	}
	read := s.Coordinator.Summary
	if view == "full" {
		read = s.Coordinator.Get
	}
	out, err := read(r.Context(), id)
	if err != nil {
		if isScanNotFound(err) {
			s.responses.Fail(w, wire.ApiErrorCodeScanNotFound, "code scan not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if out == nil {
		s.responses.Fail(w, wire.ApiErrorCodeScanNotFound, "code scan not found")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, scan.ApplyScanView(out, view))
}

// requireProjectScanID resolves {scan_id} among the {id} project's scans; a
// scan of another project's folders is not found here.
func (s *Handler) requireProjectScanID(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return "", false
	}
	id := strings.TrimSpace(chi.URLParam(r, "scan_id"))
	summary, err := s.Coordinator.Summary(r.Context(), id)
	if err != nil && !isScanNotFound(err) {
		s.responses.InternalError(w, r, err)
		return "", false
	}
	if err != nil || summary == nil || !slices.Contains(project.RootPaths(p), summary.CanonicalPath) {
		s.responses.Fail(w, wire.ApiErrorCodeScanNotFound, "code scan not found")
		return "", false
	}
	return id, true
}

func (s *Handler) HandleQueryCodeScan(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireProjectScanID(w, r)
	if !ok {
		return
	}
	var req wire.ScanQueryRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	out, err := s.Coordinator.Query(r.Context(), scan.QueryRequest{
		ScanID:          id,
		Level:           strings.TrimSpace(req.Level),
		Kind:            strings.TrimSpace(req.Kind),
		AdvisoryID:      strings.TrimSpace(req.AdvisoryID),
		Fingerprint:     strings.TrimSpace(req.Fingerprint),
		RuleID:          strings.TrimSpace(req.RuleID),
		Path:            strings.TrimSpace(req.Path),
		Code:            strings.TrimSpace(req.Code),
		Cursor:          strings.TrimSpace(req.Cursor),
		Limit:           req.Limit,
		Dedupe:          req.Dedupe,
		IntroducedSince: req.IntroducedSinceAt,
		FixedSince:      req.FixedSinceAt,
	})
	if err != nil {
		if errors.Is(err, pagecursor.ErrInvalid) || errors.Is(err, pagecursor.ErrExpired) {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		if isScanNotFound(err) {
			s.responses.Fail(w, wire.ApiErrorCodeScanNotFound, "code scan not found")
			return
		}
		if isScanNotComplete(err) {
			details := map[string]any{}
			var reject *scan.DrilldownReject
			if errors.As(err, &reject) {
				details["scan_id"], details["status"] = reject.Data["scan_id"], reject.Data["status"]
			}
			s.responses.FailDetails(w, wire.ApiErrorCodeScanNotComplete, details, "scan not complete")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) HandleExportCodeScanSARIF(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireProjectScanID(w, r)
	if !ok {
		return
	}
	out, err := s.Coordinator.Get(r.Context(), id)
	if err != nil {
		if isScanNotFound(err) {
			s.responses.Fail(w, wire.ApiErrorCodeScanNotFound, "code scan not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if out == nil {
		s.responses.Fail(w, wire.ApiErrorCodeScanNotFound, "code scan not found")
		return
	}
	switch out.Status {
	case wire.CodeScanStatusPending, wire.CodeScanStatusRunning:
		s.responses.FailDetails(w, wire.ApiErrorCodeScanNotComplete, map[string]any{"status": string(out.Status)}, "scan not complete")
		return
	case wire.CodeScanStatusFailed, wire.CodeScanStatusTimedOut,
		wire.CodeScanStatusCanceled, wire.CodeScanStatusSuperseded:
		s.responses.FailDetails(w, wire.ApiErrorCodeScanNotComplete, map[string]any{"status": string(out.Status)}, "scan did not complete")
		return
	case wire.CodeScanStatusComplete:
	}
	full := scan.ApplyScanView(out, "full")
	data, err := scanoutput.ExportScanSARIF(full)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/sarif+json")
	w.Header().Set("Content-Disposition", `attachment; filename="scan-`+id+`.sarif.json"`)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		s.responses.InternalError(w, r, err)
	}
}

func isScanNotFound(err error) bool {
	var reject *scan.DrilldownReject
	if errors.As(err, &reject) && reject != nil && reject.Code == scan.DrilldownRejectNotFound {
		return true
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return false
}

func isScanNotComplete(err error) bool {
	var reject *scan.DrilldownReject
	if errors.As(err, &reject) && reject != nil && reject.Code == scan.DrilldownRejectNotComplete {
		return true
	}
	return false
}
