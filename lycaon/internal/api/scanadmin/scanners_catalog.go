package scanadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) scannerCatalogStore(homeDir string) *scancatalog.CatalogStore {
	return scancatalog.NewCatalogStore(s.ModuleRoot, homeDir, s.reloadScannerRegistry)
}

func (s *Handler) HandleListScanners(w http.ResponseWriter, r *http.Request) {
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	cfg, rejected, err := scancatalog.LoadMergedScannerCatalog(s.ModuleRoot, "", homeDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.buildScannerListResponse(cfg, rejected, homeDir, ""))
}

func (s *Handler) HandleListProjectScanners(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	projectDir := ""
	if len(p.Roots) > 0 {
		projectDir = requestscope.GatedProjectDir(s.Settings, p, p.Roots[0].Path, projectcontrib.SurfaceScanConfig)
	}
	cfg, rejected, err := scancatalog.LoadMergedScannerCatalog(s.ModuleRoot, projectDir, homeDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.buildScannerListResponse(cfg, rejected, homeDir, projectDir))
}

func (s *Handler) HandleCreateScanner(w http.ResponseWriter, r *http.Request) {
	if err := httpio.RequireRequestMediaType(r, httpio.MediaTypeJSON); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	body, err := httpio.ReadAllBody(w, r)
	if err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	// source selects the variant each branch then decodes strictly.
	var probe struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	switch strings.TrimSpace(probe.Source) {
	case "catalog":
		var req wire.CreateCatalogScannerRequest
		if err := httpio.DecodeStrictJSON(bytes.NewReader(body), &req); err != nil {
			s.responses.DecodeError(w, r, err)
			return
		}
		cat, _, ok := s.loadScannerCatalog(w, r)
		if !ok {
			return
		}
		catalogID := strings.TrimSpace(req.CatalogID)
		entry, found := cat.ScannerEntryFor(catalogID)
		if !found {
			s.responses.Fail(w, wire.ApiErrorCodeScannerCatalogEntryNotFound, "unknown scanner catalog id "+catalogID)
			return
		}
		if err := s.scannerCatalogStore(homeDir).CreateUserExternalScanner(r.Context(), entry); err != nil {
			s.writeScannerStoreError(w, r, err)
			return
		}
		sum, ok := s.lookupScannerSummary(w, r, entry.ID, "", homeDir)
		if !ok {
			return
		}
		httpio.WriteJSON(w, http.StatusCreated, sum)
	case "custom":
		var req wire.CreateCustomScannerRequest
		if err := httpio.DecodeStrictJSON(bytes.NewReader(body), &req); err != nil {
			s.responses.DecodeError(w, r, err)
			return
		}
		entry := scancatalog.ScannerEntry{
			ID:                  strings.TrimSpace(req.ID),
			Driver:              scancatalog.DriverExternal,
			Engine:              strings.TrimSpace(req.Engine),
			ScopeKind:           strings.TrimSpace(req.ScopeKind),
			Command:             append([]string(nil), req.Command...),
			OutputParser:        strings.TrimSpace(req.OutputParser),
			MapperID:            strings.TrimSpace(req.MapperID),
			Categories:          append([]string(nil), req.Categories...),
			Label:               strings.TrimSpace(req.Label),
			Description:         strings.TrimSpace(req.Description),
			SkipIfBinaryMissing: req.SkipIfBinaryMissing,
			Env:                 append([]string(nil), req.Env...),
		}
		if entry.Runtime, err = scanRuntimePolicy(req.Runtime); err != nil {
			s.writeScannerStoreError(w, r, err)
			return
		}
		if err := s.scannerCatalogStore(homeDir).CreateUserExternalScanner(r.Context(), entry); err != nil {
			s.writeScannerStoreError(w, r, err)
			return
		}
		sum, ok := s.lookupScannerSummary(w, r, entry.ID, "", homeDir)
		if !ok {
			return
		}
		httpio.WriteJSON(w, http.StatusCreated, sum)
	default:
		// Neither variant: a body carrying more than the discriminator is not
		// a create request at all.
		if err := httpio.DecodeStrictJSON(bytes.NewReader(body), &probe); err != nil {
			s.responses.DecodeError(w, r, err)
			return
		}
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{
			"field":  "source",
			"reason": "must be catalog or custom",
		}, "source must be catalog or custom")
	}
}

func (s *Handler) HandleUpdateScanner(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "scanner_id"))
	var req wire.UpdateScannerRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	// The definition replaces an existing scanner, so the address resolves
	// before the replacement is judged.
	if !s.requireScanner(w, r, id, homeDir) {
		return
	}
	if err := s.applyUpdateScanner(r.Context(), id, homeDir, req); err != nil {
		s.writeScannerStoreError(w, r, err)
		return
	}
	sum, ok := s.lookupScannerSummary(w, r, id, "", homeDir)
	if !ok {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, sum)
}

func (s *Handler) requireScanner(w http.ResponseWriter, r *http.Request, id, homeDir string) bool {
	cfg, _, err := scancatalog.LoadMergedScannerCatalog(s.ModuleRoot, "", homeDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return false
	}
	for _, entry := range cfg.Scanners {
		if entry.ID == id {
			return true
		}
	}
	s.responses.Fail(w, wire.ApiErrorCodeScannerNotFound, "scanner not found")
	return false
}

func (s *Handler) applyUpdateScanner(ctx context.Context, id, homeDir string, req wire.UpdateScannerRequest) error {
	fullReplace := strings.TrimSpace(req.Engine) != "" || strings.TrimSpace(req.ScopeKind) != "" ||
		len(req.Command) > 0 || strings.TrimSpace(req.OutputParser) != "" ||
		strings.TrimSpace(req.Driver) != "" || len(req.Categories) > 0 ||
		strings.TrimSpace(req.MapperID) != "" || req.Env != nil ||
		req.SkipIfBinaryMissing != nil || req.Runtime != nil ||
		strings.TrimSpace(req.Label) != "" || strings.TrimSpace(req.Description) != ""

	if !fullReplace {
		return scanStoreInvalid("scanner fields required")
	}

	driver := strings.TrimSpace(req.Driver)
	if driver == "" {
		driver = scancatalog.DriverExternal
	}
	if driver != scancatalog.DriverExternal {
		return &scancatalog.CatalogStoreError{
			Code:    scancatalog.RejectCommunityDriverForbidden,
			Message: "host stock may only be replaced by a complete external definition",
		}
	}
	if strings.TrimSpace(req.Engine) == "" || strings.TrimSpace(req.ScopeKind) == "" ||
		len(req.Command) == 0 || strings.TrimSpace(req.OutputParser) == "" || len(req.Categories) == 0 || req.Runtime == nil {
		return scanStoreInvalid("full external replace requires engine, scope_kind, command, output_parser, categories, and runtime")
	}
	entry := scancatalog.ScannerEntry{
		ID:                  id,
		Driver:              scancatalog.DriverExternal,
		Engine:              strings.TrimSpace(req.Engine),
		ScopeKind:           strings.TrimSpace(req.ScopeKind),
		Command:             append([]string(nil), req.Command...),
		OutputParser:        strings.TrimSpace(req.OutputParser),
		MapperID:            strings.TrimSpace(req.MapperID),
		Categories:          append([]string(nil), req.Categories...),
		Label:               strings.TrimSpace(req.Label),
		Description:         strings.TrimSpace(req.Description),
		SkipIfBinaryMissing: req.SkipIfBinaryMissing,
		Env:                 append([]string(nil), req.Env...),
	}
	var err error
	if entry.Runtime, err = scanRuntimePolicy(req.Runtime); err != nil {
		return err
	}
	return s.scannerCatalogStore(homeDir).SaveDeviceScannerEntry(ctx, entry)
}

// runtimeFieldError names the request field a runtime policy refusal is about.
type runtimeFieldError struct {
	field, message string
}

func (e *runtimeFieldError) Error() string { return e.field + ": " + e.message }

// scanRuntimePolicy converts the wire policy. The catalog keeps whole seconds,
// so a limit with a sub-second remainder is refused rather than truncated.
func scanRuntimePolicy(runtime *wire.ScanRuntimePolicy) (scancatalog.RuntimePolicy, error) {
	if runtime == nil {
		return scancatalog.RuntimePolicy{}, &runtimeFieldError{field: "runtime", message: "runtime is required"}
	}
	if runtime.SoftLimitMs%1000 != 0 {
		return scancatalog.RuntimePolicy{}, &runtimeFieldError{field: "runtime.soft_limit_ms", message: "the soft limit must be a whole number of seconds"}
	}
	if runtime.HardLimitMs%1000 != 0 {
		return scancatalog.RuntimePolicy{}, &runtimeFieldError{field: "runtime.hard_limit_ms", message: "the hard limit must be a whole number of seconds"}
	}
	policy := scancatalog.RuntimePolicy{
		SoftLimitSec: runtime.SoftLimitMs / 1000,
		HardLimitSec: runtime.HardLimitMs / 1000,
		CPUUnits:     runtime.CPUUnits,
		Parallelism:  runtime.Parallelism,
	}
	if err := scancatalog.ValidateRuntimePolicy(policy); err != nil {
		return scancatalog.RuntimePolicy{}, &runtimeFieldError{field: "runtime", message: "the runtime policy is out of range"}
	}
	return policy, nil
}

func scanStoreInvalid(msg string) error {
	return &scancatalog.CatalogStoreError{Code: scancatalog.RejectInvalidEntry, Message: msg}
}

func (s *Handler) HandleDeleteScanner(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "scanner_id"))
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if err := s.scannerCatalogStore(homeDir).DeleteUserScannerRow(r.Context(), id); err != nil {
		if errors.Is(err, scancatalog.ErrScannerNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeScannerNotFound, "user scanner overlay not found")
			return
		}
		s.writeScannerStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) reloadScannerRegistry() error {
	reloader, ok := s.Registry.(interface{ Reload() error })
	if !ok {
		return nil
	}
	return reloader.Reload()
}

func (s *Handler) HandleUpdateProjectScanner(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	if !requestscope.ProjectSurfaceApplies(s.Settings, p, projectcontrib.SurfaceScanConfig) {
		s.responses.Fail(w, wire.ApiErrorCodeTrustSurfaceOff, "project scan reporting is off — open Trust and turn it on")
		return
	}
	scannerID := strings.TrimSpace(chi.URLParam(r, "scanner_id"))
	if scannerID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "scanner id required")
		return
	}
	var req wire.UpdateProjectScannerRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Enabled == nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "enabled"}, "enabled is required")
		return
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	projectDir := project.PrimaryRootPath(p)
	if err := s.scannerCatalogStore(homeDir).SetProjectScannerEnabled(r.Context(), projectDir, scannerID, *req.Enabled); err != nil {
		s.writeScannerStoreError(w, r, err)
		return
	}
	sum, ok := s.lookupScannerSummary(w, r, scannerID, projectDir, homeDir)
	if !ok {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, sum)
}

func (s *Handler) lookupScannerSummary(w http.ResponseWriter, r *http.Request, id, projectDir, homeDir string) (wire.ScannerSummary, bool) {
	cfg, rejected, err := scancatalog.LoadMergedScannerCatalog(s.ModuleRoot, projectDir, homeDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return wire.ScannerSummary{}, false
	}
	resp := s.buildScannerListResponse(cfg, rejected, homeDir, projectDir)
	for _, sum := range resp.Scanners {
		if sum.ID == id {
			return sum, true
		}
	}
	s.responses.Fail(w, wire.ApiErrorCodeScannerNotFound, "scanner not found after write")
	return wire.ScannerSummary{}, false
}

func (s *Handler) buildScannerListResponse(cfg *scancatalog.ScannerConfig, rejected []scancatalog.RejectedRow, homeDir, projectDir string) wire.ScannerListResponse {
	reports := scancatalog.CheckCatalog(cfg, s.ModuleRoot, projectDir)
	byID := make(map[string]scancatalog.ScannerEntry, len(cfg.Scanners))
	for _, e := range cfg.Scanners {
		byID[e.ID] = e
	}
	out := make([]wire.ScannerSummary, 0, len(reports))
	for _, rep := range reports {
		entry := byID[rep.ID]
		contract := entry.Contract()
		label := strings.TrimSpace(entry.Label)
		if label == "" {
			label = entry.ID
		}
		sum := wire.ScannerSummary{
			ID:             rep.ID,
			Driver:         rep.Driver,
			Engine:         contract.Engine,
			ScopeKind:      string(contract.Scope),
			Categories:     entry.CategoriesAPI(),
			Enabled:        rep.Enabled,
			CheckOK:        rep.CheckOK,
			BinaryFound:    rep.BinaryFound,
			RequiresBinary: scancatalog.RequiresBinaryLabel(entry.Command),
			OutputParser:   strings.TrimSpace(entry.OutputParser),
			CatalogSource:  string(entry.CatalogSource),
			Issues:         scancatalog.CheckIssueCodes(rep),
			CommandSummary: scancatalog.CommandSummary(entry.Command),
			Label:          label,
			Description:    strings.TrimSpace(entry.Description),
			MapperID:       strings.TrimSpace(entry.MapperID),
			Runtime: wire.ScanRuntimePolicy{
				SoftLimitMs: entry.RuntimePolicy().SoftLimitSec * 1000,
				HardLimitMs: entry.RuntimePolicy().HardLimitSec * 1000,
				CPUUnits:    entry.RuntimePolicy().CPUUnits,
				Parallelism: entry.RuntimePolicy().Parallelism,
			},
		}
		if sum.Driver != scancatalog.DriverExternal {
			sum.RequiresBinary = ""
			sum.CommandSummary = ""
			sum.OutputParser = ""
			sum.MapperID = ""
		}
		out = append(out, sum)
	}
	refused := make(map[string]wire.ScannerRejectedRow, len(rejected))
	for _, row := range rejected {
		refused[row.ID] = wire.ScannerRejectedRow{ID: row.ID, Code: row.Code, Detail: row.Detail}
	}
	return wire.ScannerListResponse{
		Scanners:         out,
		Rejected:         refused,
		UserScannersPath: scancatalog.UserScannersPath(homeDir),
	}
}

func (s *Handler) writeScannerStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, scancatalog.ErrScannerExists) {
		s.responses.Fail(w, wire.ApiErrorCodeDuplicateId, "a scanner with this id already exists")
		return
	}
	if errors.Is(err, scancatalog.ErrScannerNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeScannerNotFound, "scanner not found")
		return
	}
	var fieldErr *runtimeFieldError
	if errors.As(err, &fieldErr) {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": fieldErr.field}, fieldErr.message)
		return
	}
	var se *scancatalog.CatalogStoreError
	if errors.As(err, &se) {
		msg := se.Message
		switch se.Code {
		case scancatalog.RejectInvalidEntry:
			s.responses.Fail(w, wire.ApiErrorCodeInvalidEntry, msg)
		case scancatalog.RejectCommunityDriverForbidden:
			s.responses.Fail(w, wire.ApiErrorCodeCommunityDriverForbidden, msg)
		case scancatalog.RejectCommunityParserForbidden:
			s.responses.Fail(w, wire.ApiErrorCodeCommunityParserForbidden, msg)
		case scancatalog.RejectMapperMissing:
			s.responses.Fail(w, wire.ApiErrorCodeMapperMissing, msg)
		case scancatalog.RejectProjectFieldsForbidden:
			s.responses.Fail(w, wire.ApiErrorCodeProjectFieldsForbidden, msg)
		case scancatalog.RejectArgvProjectPathForbidden:
			s.responses.Fail(w, wire.ApiErrorCodeArgvProjectPathForbidden, msg)
		case scancatalog.RejectArgvShellForbidden:
			s.responses.Fail(w, wire.ApiErrorCodeArgvShellForbidden, msg)
		case scancatalog.RejectDuplicateID:
			s.responses.Fail(w, wire.ApiErrorCodeDuplicateId, msg)
		default:
			s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"reject_code": se.Code}, msg)
		}
		return
	}
	s.responses.InternalError(w, r, err)
}
