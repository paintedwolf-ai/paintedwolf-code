package scanadmin

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/configdir"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleListScannerCatalog(w http.ResponseWriter, r *http.Request) {
	cat, homeDir, ok := s.loadScannerCatalog(w, r)
	if !ok {
		return
	}
	installed, err := scancatalog.LoadUserScannerOverlay(scancatalog.UserScannersPath(homeDir))
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	added := make(map[string]struct{}, len(installed.Scanners))
	for _, entry := range installed.Scanners {
		added[entry.ID] = struct{}{}
	}

	entries := cat.Entries()
	rows := make([]wire.ScannerCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		categories := make([]wire.ScanCategory, 0, len(entry.Categories))
		for _, c := range entry.Categories {
			categories = append(categories, wire.ScanCategory(c))
		}
		_, isAdded := added[entry.ID]
		rows = append(rows, wire.ScannerCatalogEntry{
			ID:             entry.ID,
			Label:          entry.Label,
			Binary:         entry.Binary,
			Categories:     categories,
			Hint:           entry.Hint,
			Install:        entry.Install,
			DocsURL:        entry.DocsURL,
			Env:            append([]string(nil), entry.Env...),
			CommandSummary: scancatalog.CommandSummary(entry.Command),
			Added:          isAdded,
			BinaryFound:    scancatalog.BinaryOnPath(entry.Command),
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ScannerCatalogResponse{Scanners: rows})
}

func (s *Handler) HandleScannerCheck(w http.ResponseWriter, r *http.Request) {
	cat, homeDir, ok := s.loadScannerCatalog(w, r)
	if !ok {
		return
	}
	cfg, _, err := scancatalog.LoadMergedScannerCatalog(s.ModuleRoot, "", homeDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}

	// Built-in checks do not require a process probe.
	builtInReports := make(map[string]scancatalog.ScannerCheckReport)
	for _, report := range scancatalog.CheckCatalog(cfg, s.ModuleRoot, "") {
		builtInReports[report.ID] = report
	}

	rows := make([]wire.ScannerCheckRow, 0, len(cfg.Scanners))
	for _, entry := range cfg.Scanners {
		if strings.TrimSpace(entry.Driver) != scancatalog.DriverExternal {
			report, ok := builtInReports[entry.ID]
			if !ok {
				continue
			}
			faults := builtInFaults(report)
			rows = append(rows, wire.ScannerCheckRow{
				ScannerID:   entry.ID,
				OK:          len(faults) == 0,
				Detail:      builtInCheckDetail(faults),
				BinaryFound: true,
			})
			continue
		}
		probe := entry.Command
		if known, ok := cat.Entry(entry.ID); ok && len(known.Probe) > 0 {
			probe = known.Probe
		}
		res := scancatalog.ProbeScanner(r.Context(), entry.ID, probe)
		rows = append(rows, wire.ScannerCheckRow{
			ScannerID:   res.ScannerID,
			OK:          res.OK,
			Detail:      res.Detail,
			BinaryFound: res.BinaryFound,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ScannerCheckResponse{Rows: rows})
}

// A slot has one selected scanner.
func (s *Handler) HandleReplaceScannerSlot(w http.ResponseWriter, r *http.Request) {
	var req wire.SelectScannerSlotRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	category := strings.TrimSpace(chi.URLParam(r, "category"))
	if !scancatalog.IsSlotCategory(category) {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "category", "reason": "unknown scanner slot"}, "unknown scanner slot")
		return
	}
	if err := s.scannerCatalogStore(homeDir).SelectScannerForCategory(
		r.Context(), category, strings.TrimSpace(req.ScannerID),
	); err != nil {
		s.writeScannerStoreError(w, r, err)
		return
	}
	cfg, rejected, err := scancatalog.LoadMergedScannerCatalog(s.ModuleRoot, "", homeDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.buildScannerListResponse(cfg, rejected, homeDir, ""))
}

// Unselected built-in scanners are healthy.
func builtInFaults(report scancatalog.ScannerCheckReport) []string {
	var faults []string
	for _, issue := range report.Issues {
		if issue.Code == scancatalog.ScannerDiagnosticDisabled {
			continue
		}
		faults = append(faults, issue.Detail)
	}
	return faults
}

func builtInCheckDetail(faults []string) string {
	if len(faults) == 0 {
		return "Built in"
	}
	return strings.Join(faults, "; ")
}

func (s *Handler) loadScannerCatalog(w http.ResponseWriter, r *http.Request) (*scancatalog.ScannerCatalog, string, bool) {
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return nil, "", false
	}
	cat, err := scancatalog.LoadScannerCatalog()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return nil, "", false
	}
	return cat, homeDir, true
}
