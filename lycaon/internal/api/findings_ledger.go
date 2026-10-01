package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectignore"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const ledgerExportLimit = 5000

func (s *Server) handleQueryProjectFindings(w http.ResponseWriter, r *http.Request) {
	p, root, ok := s.requireLedgerProject(w, r)
	if !ok {
		return
	}
	var req wire.FindingLedgerQueryRequest
	if _, err := httpio.DecodeOptionalJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	out, err := s.scanCadence.FindingLedger(r.Context(), root, req)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out.ProjectID = p.ID
	if out.Entries == nil {
		out.Entries = []wire.FindingLedgerEntry{}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleListProjectFindingIgnores(w http.ResponseWriter, r *http.Request) {
	p, root, ok := s.requireLedgerProject(w, r)
	if !ok {
		return
	}
	out, err := s.scanCadence.ListIgnores(r.Context(), root)
	if err != nil {
		s.writeIgnoreError(w, r, err)
		return
	}
	out.ProjectID = p.ID
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateProjectFindingIgnore(w http.ResponseWriter, r *http.Request) {
	p, root, ok := s.requireLedgerProject(w, r)
	if !ok {
		return
	}
	var entry wire.FindingIgnoreEntry
	if err := httpio.DecodeJSON(w, r, &entry); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	out, err := s.scanCadence.AddIgnore(r.Context(), root, entry)
	if err != nil {
		s.writeIgnoreError(w, r, err)
		return
	}
	out.ProjectID = p.ID
	httpio.WriteJSON(w, http.StatusCreated, out)
}

func (s *Server) handleDeleteProjectFindingIgnore(w http.ResponseWriter, r *http.Request) {
	_, root, ok := s.requireLedgerProject(w, r)
	if !ok {
		return
	}
	if _, err := s.scanCadence.RemoveIgnore(r.Context(), root, chi.URLParam(r, "entry_id")); err != nil {
		s.writeIgnoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleExportProjectFindings(w http.ResponseWriter, r *http.Request) {
	p, root, ok := s.requireLedgerProject(w, r)
	if !ok {
		return
	}
	var req wire.FindingExportRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	query := req.Query
	query.Cursor = ""
	query.Limit = ledgerExportLimit
	page, err := s.scanCadence.FindingLedger(r.Context(), root, query)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	switch req.Format {
	case wire.FindingExportSARIF:
		data, err := scanoutput.ExportLedgerSARIF(page.Entries)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		writeExportDocument(w, "application/sarif+json", p.Name+"-findings.sarif", data)
	case wire.FindingExportOpenVEX:
		data, err := scanoutput.ExportLedgerOpenVEX(page.Entries, p.Name, time.Now().UTC())
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		writeExportDocument(w, "application/json", p.Name+"-findings.openvex.json", data)
	default:
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{
			"field":  "format",
			"reason": "export format is not a known value",
		}, "export format is not a known value")
	}
}

func writeExportDocument(w http.ResponseWriter, contentType, filename string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", exportFilename(filename)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// exportFilename strips characters that could break the Content-Disposition header.
func exportFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(name))
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "findings"
	}
	return cleaned
}

// A project without a root returns a conflict.
func (s *Server) requireLedgerProject(w http.ResponseWriter, r *http.Request) (*project.Project, string, bool) {
	p, ok := requestscope.ProjectByURLID(s.projectRegistry, &s.responses, w, r)
	if !ok {
		return nil, "", false
	}
	roots, ok := s.Scan.RequireScanRoots(w, r, p, false)
	if !ok {
		return nil, "", false
	}
	return p, roots[0], true
}

func (s *Server) ignoreFieldInvalid(w http.ResponseWriter, field, reason string) {
	s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": field, "reason": reason}, reason)
}

func (s *Server) writeIgnoreError(w http.ResponseWriter, r *http.Request, err error) {
	if s.responses.OverlayFormatError(w, err) {
		return
	}
	switch {
	case errors.Is(err, projectignore.ErrConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIgnoreFileChanged, "ignore file changed; reload before saving")
	case errors.Is(err, scanignore.ErrIgnoreEntryNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeIgnoreEntryNotFound, "ignore entry not found")
	case errors.Is(err, scanignore.ErrIgnoreProjectNotTrusted):
		s.responses.Fail(w, wire.ApiErrorCodeTrustSurfaceOff, "project scanning is not trusted")
	case errors.Is(err, projectignore.ErrInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "the project ignore file is not valid")
	case errors.Is(err, scanignore.ErrIgnoreNoPredicate):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "an ignore entry must name what it matches")
	case errors.Is(err, scanignore.ErrIgnoreNoReason):
		s.ignoreFieldInvalid(w, "reason", "an ignore entry needs a reason")
	case errors.Is(err, scanignore.ErrIgnoreJustificationScope):
		s.ignoreFieldInvalid(w, "justification", "a justification requires an advisory")
	case errors.Is(err, scanignore.ErrIgnoreJustificationInvalid):
		s.ignoreFieldInvalid(w, "justification", "the justification is not a known value")
	case errors.Is(err, scanignore.ErrIgnoreExpiryInvalid):
		s.ignoreFieldInvalid(w, "expires_on", "the expiry must be a calendar date")
	case errors.Is(err, scanignore.ErrIgnoreKindInvalid):
		s.ignoreFieldInvalid(w, "kind", "the kind is not a known finding kind")
	default:
		s.responses.InternalError(w, r, err)
	}
}
