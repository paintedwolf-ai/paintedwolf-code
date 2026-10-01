package scanadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleStartFullScan(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	var req wire.FullScanRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	out := wire.FullScanResponse{Passes: []wire.SecurityFullPass{}}
	roots, ok := s.RequireScanRoots(w, r, p, true)
	if !ok {
		return
	}
	for _, root := range roots {
		pass, err := s.Cadence.RequestFull(r.Context(), root, req.ScannerIDs, wire.ScanTriggerManual, scan.FullScanContext{
			SessionID: strings.TrimSpace(req.SessionID),
		})
		if err != nil {
			s.writeFullScanError(w, r, err)
			return
		}
		out.Passes = append(out.Passes, pass.Wire())
	}
	root := project.PrimaryRootPath(p)
	if len(roots) == 1 {
		root = roots[0]
	}
	overview, err := s.projectSecurityOverview(r, p, root)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out.Overview = overview
	httpio.WriteJSON(w, http.StatusAccepted, out)
}

func (s *Handler) HandleGetProjectSecurity(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	roots, ok := s.RequireScanRoots(w, r, p, false)
	if !ok {
		return
	}
	overview, err := s.projectSecurityOverview(r, p, roots[0])
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, overview)
}

func (s *Handler) projectSecurityOverview(r *http.Request, p *project.Project, root string) (wire.SecurityOverview, error) {
	out := wire.SecurityOverview{ProjectID: p.ID, Scanners: []wire.SecurityScannerState{}}
	if root == "" {
		return out, nil
	}
	overview, err := s.Cadence.SecurityOverview(r.Context(), root, s.scannerLabels(r, p))
	if err != nil {
		return out, err
	}
	overview.ProjectID = p.ID
	return overview, nil
}

// Unavailable catalog labels fall back to scanner IDs.
func (s *Handler) scannerLabels(r *http.Request, p *project.Project) map[string]string {
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return nil
	}
	projectDir := requestscope.GatedProjectDir(s.Settings, p, project.PrimaryRootPath(p), projectcontrib.SurfaceScanConfig)
	cfg, _, err := scancatalog.LoadMergedScannerCatalog(s.ModuleRoot, projectDir, homeDir)
	if err != nil {
		return nil
	}
	labels := make(map[string]string, len(cfg.Scanners))
	for _, entry := range cfg.Scanners {
		if label := strings.TrimSpace(entry.Label); label != "" {
			labels[entry.ID] = label
		}
	}
	return labels
}

func (s *Handler) writeFullScanError(w http.ResponseWriter, r *http.Request, err error) {
	var selection *scancatalog.ErrInvalidScanEngineSelection
	switch {
	case errors.Is(err, scan.ErrSecurityScannersOff):
		s.responses.Fail(w, wire.ApiErrorCodeSecurityDisabled, settings.SecurityScannersOffDetail())
	case errors.Is(err, scan.ErrNoScannerAvailable):
		s.responses.Fail(w, wire.ApiErrorCodeNoScannerAvailable, "no scanner is available for this folder")
	case errors.As(err, &selection):
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest,
			map[string]any{"field": "scanner_ids", "reason": "names a scanner this project does not select"},
			"scanner_ids names a scanner this project does not select")
	default:
		s.responses.InternalError(w, r, err)
	}
}
