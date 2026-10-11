package searchadmin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/search"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleExportSearchResults(w http.ResponseWriter, r *http.Request) {
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
	compileCtx.IncludeDependencies = req.IncludeDependencies
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
