package catalog

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
)

// MergeScannerCatalog merges host → user → project (enable-only) with trust gates.
// Rejected rows are omitted from entries and returned separately.
func MergeScannerCatalog(host, user, project *ScannerConfig, opts MergeOptions) (entries []ScannerEntry, rejected []RejectedRow, err error) {
	m := &catalogMerger{
		opts: opts,
		byID: make(map[string]ScannerEntry),
	}
	if host != nil {
		m.mergeHostLayer(host.Scanners)
	}
	if user != nil {
		m.mergeUserLayer(user.Scanners)
	}
	// A nil project means project scan configuration does not apply.
	if project != nil {
		m.applyProjectEnableOverlay(project.Scanners)
	}
	m.enforceSlotExclusivity()
	out := make([]ScannerEntry, 0, len(m.order))
	for _, id := range m.order {
		if e, ok := m.byID[id]; ok {
			out = append(out, e)
		}
	}
	return out, m.rejected, nil
}

// enforceSlotExclusivity keeps malformed hand-edited overlays from producing
// two runnable scanners for one job. Normal writes update a whole slot
// atomically; this covers external edits.
func (m *catalogMerger) enforceSlotExclusivity() {
	selected := make(map[string]string, len(SlotCategories()))
	for _, id := range m.order {
		entry, ok := m.byID[id]
		if !ok || !entry.EnabledOrDefault() {
			continue
		}
		slot := PrimaryCategory(entry.Categories)
		if previous, conflict := selected[slot]; conflict {
			disabled := false
			entry.Enabled = &disabled
			m.byID[id] = entry
			m.reject(id, RejectSlotConflict,
				fmt.Sprintf("scanner slot %q is already assigned to %q", slot, previous))
			continue
		}
		selected[slot] = id
	}
}

type catalogMerger struct {
	opts     MergeOptions
	byID     map[string]ScannerEntry
	order    []string
	rejected []RejectedRow
}

func (m *catalogMerger) put(e ScannerEntry, source CatalogSource) {
	e.CatalogSource = source
	if _, exists := m.byID[e.ID]; !exists {
		m.order = append(m.order, e.ID)
	}
	m.byID[e.ID] = e
}

func (m *catalogMerger) reject(id, code, detail string) {
	m.rejected = append(m.rejected, RejectedRow{ID: id, Code: code, Detail: detail})
}

// dedupeID trims and duplicate-checks a row id within one layer; empty string
// means the row was rejected.
func (m *catalogMerger) dedupeID(rawID, layer string, seen map[string]struct{}) string {
	id := strings.TrimSpace(rawID)
	if id == "" {
		m.reject("", RejectInvalidEntry, layer+" entry missing id")
		return ""
	}
	if _, dup := seen[id]; dup {
		m.reject(id, RejectDuplicateID, "duplicate "+layer+" id")
		return ""
	}
	seen[id] = struct{}{}
	return id
}

func (m *catalogMerger) mergeHostLayer(rows []ScannerEntry) {
	seen := make(map[string]struct{}, len(rows))
	for _, raw := range rows {
		e := raw
		id := m.dedupeID(e.ID, "host", seen)
		if id == "" {
			continue
		}
		e.ID = id
		if err := validateScannerEntry(e); err != nil {
			m.reject(id, RejectInvalidEntry, err.Error())
			continue
		}
		if strings.TrimSpace(e.Driver) == DriverExternal {
			if err := ValidateExternalArgv(e.Command, m.opts.WorkspaceRoots); err != nil {
				m.reject(id, catalogErrorCode(err), catalogErrorDetail(err))
				continue
			}
		}
		m.put(e, CatalogSourceHost)
	}
}

func (m *catalogMerger) mergeUserLayer(rows []ScannerEntry) {
	seen := make(map[string]struct{}, len(rows))
	for _, raw := range rows {
		e := raw
		id := m.dedupeID(e.ID, "user", seen)
		if id == "" {
			continue
		}
		e.ID = id

		if isEnableOnlyRow(e) {
			existing, ok := m.byID[id]
			if !ok {
				m.reject(id, RejectInvalidEntry, "user enable-only for unknown id")
				continue
			}
			existing.Enabled = e.Enabled
			existing.CatalogSource = CatalogSourceUser
			m.byID[id] = existing
			continue
		}

		if !m.userFullRowAllowed(e) {
			continue
		}
		if err := validateScannerEntry(e); err != nil {
			m.reject(id, RejectInvalidEntry, err.Error())
			continue
		}
		if strings.TrimSpace(e.Driver) == DriverExternal {
			if err := ValidateExternalArgv(e.Command, m.opts.WorkspaceRoots); err != nil {
				m.reject(id, catalogErrorCode(err), catalogErrorDetail(err))
				continue
			}
		}
		m.put(e, CatalogSourceUser)
	}
}

// userFullRowAllowed enforces user-class trust gates on a full (non enable-only)
// row: driver class, no impl, and map/json mapper presence. Rejections are
// recorded and false returned.
func (m *catalogMerger) userFullRowAllowed(e ScannerEntry) bool {
	id := e.ID
	_, exists := m.byID[id]
	driver := strings.TrimSpace(e.Driver)
	if !exists {
		if driver != DriverExternal {
			m.reject(id, RejectCommunityDriverForbidden, "user may only add external drivers for new ids")
			return false
		}
	} else if driver == DriverLibrary || driver == DriverBundled {
		m.reject(id, RejectCommunityDriverForbidden, "user may not set library/bundled driver")
		return false
	}
	if driver != DriverExternal {
		return true
	}
	if strings.TrimSpace(e.Impl) != "" {
		m.reject(id, RejectInvalidEntry, "external driver must not set impl")
		return false
	}
	if parser := strings.TrimSpace(e.OutputParser); !scanoutput.IsUserExternalParserAllowed(parser) {
		m.reject(id, RejectCommunityParserForbidden,
			fmt.Sprintf("output_parser %q not allowed for user external scanners", parser))
		return false
	}
	if strings.TrimSpace(e.OutputParser) == scanoutput.OutputParserMapJSON {
		mapperID := strings.TrimSpace(e.MapperID)
		if mapperID == "" {
			m.reject(id, RejectMapperMissing, "mapper_id required for map/json parser")
			return false
		}
		if _, err := scanoutput.LoadMapper(m.opts.UserConfigDir, mapperID); err != nil {
			m.reject(id, RejectMapperMissing, err.Error())
			return false
		}
	}
	return true
}

func (m *catalogMerger) applyProjectEnableOverlay(rows []ScannerEntry) {
	seen := make(map[string]struct{}, len(rows))
	for _, raw := range rows {
		e := raw
		id := m.dedupeID(e.ID, "project", seen)
		if id == "" {
			continue
		}
		e.ID = id
		if !isEnableOnlyRow(e) {
			m.reject(id, RejectProjectFieldsForbidden, "project scanners.yaml may only set id and enabled")
			continue
		}
		existing, ok := m.byID[id]
		if !ok {
			m.reject(id, RejectProjectUnknownID, "project enable for unknown scanner id")
			continue
		}
		existing.Enabled = e.Enabled
		existing.CatalogSource = CatalogSourceProject
		m.byID[id] = existing
	}
}

// isEnableOnlyRow accepts only project-controlled fields.
func isEnableOnlyRow(e ScannerEntry) bool {
	if strings.TrimSpace(e.Driver) != "" {
		return false
	}
	if strings.TrimSpace(e.Impl) != "" {
		return false
	}
	if strings.TrimSpace(e.Engine) != "" {
		return false
	}
	if strings.TrimSpace(e.ScopeKind) != "" {
		return false
	}
	if len(e.Categories) > 0 {
		return false
	}
	if strings.TrimSpace(e.Config) != "" {
		return false
	}
	if len(e.Command) > 0 {
		return false
	}
	if strings.TrimSpace(e.OutputParser) != "" {
		return false
	}
	if strings.TrimSpace(e.MapperID) != "" {
		return false
	}
	if strings.TrimSpace(e.Workdir) != "" {
		return false
	}
	if e.Runtime != (RuntimePolicy{}) {
		return false
	}
	if e.SkipIfBinaryMissing != nil {
		return false
	}
	if len(e.Env) > 0 {
		return false
	}
	if len(e.OkExitCodes) > 0 {
		return false
	}
	if strings.TrimSpace(e.Label) != "" {
		return false
	}
	if strings.TrimSpace(e.Description) != "" {
		return false
	}
	return true
}

func catalogErrorCode(err error) string {
	if err == nil {
		return RejectInvalidEntry
	}
	var coded *CatalogStoreError
	if errors.As(err, &coded) && coded.Code != "" {
		return coded.Code
	}
	return RejectInvalidEntry
}

func catalogErrorDetail(err error) string {
	var coded *CatalogStoreError
	if errors.As(err, &coded) {
		return coded.Message
	}
	return err.Error()
}

// UserScannersPath returns {configDir}/scanners.yaml (device layer).
func UserScannersPath(configDir string) string {
	return filepath.Join(configDir, "scanners.yaml")
}

// LoadMergedScannerConfig merges host, user, and project scanner catalogs with trust gates.
// Rejected rows are omitted from the returned config.
func LoadMergedScannerConfig(moduleRoot, projectDir, homeDir string) (*ScannerConfig, error) {
	merged, _, err := LoadMergedScannerCatalog(moduleRoot, projectDir, homeDir)
	return merged, err
}

// LoadMergedScannerCatalog merges catalogs and returns rejected trust-gate rows.
func LoadMergedScannerCatalog(moduleRoot, projectDir, homeDir string) (*ScannerConfig, []RejectedRow, error) {
	moduleRoot = strings.TrimSpace(moduleRoot)
	if moduleRoot == "" {
		return nil, nil, fmt.Errorf("module root required")
	}
	if strings.TrimSpace(homeDir) == "" {
		var err error
		homeDir, err = configdir.UserConfigDir()
		if err != nil {
			return nil, nil, err
		}
	}
	host, err := LoadScannerConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("bundled scanners: %w", err)
	}

	userCfg, err := loadOptionalScannerConfig(UserScannersPath(homeDir))
	if err != nil {
		return nil, nil, err
	}

	var projectCfg *ScannerConfig
	// An unreadable overlay becomes a rejected row, so Settings can say the
	// person's lines had no effect instead of silently omitting them.
	var projectRejected []RejectedRow
	if projectDir = strings.TrimSpace(projectDir); projectDir != "" {
		projectCfg, err = loadOptionalScannerConfig(ProjectScannersPath(projectDir))
		if err != nil {
			projectCfg = nil
			projectRejected = append(projectRejected, RejectedRow{
				Code:   RejectProjectUnreadable,
				Detail: fmt.Sprintf("could not read %s: %v", ProjectScannersRelPath(), err),
			})
		}
	}

	roots := []string{}
	if projectDir != "" {
		roots = append(roots, projectDir)
	}
	entries, rejected, err := MergeScannerCatalog(host, userCfg, projectCfg, MergeOptions{
		WorkspaceRoots: roots,
		UserConfigDir:  homeDir,
	})
	if err != nil {
		return nil, nil, err
	}
	merged := &ScannerConfig{Scanners: entries}
	if err := ValidateScannerConfig(merged); err != nil {
		return nil, nil, err
	}
	return merged, append(projectRejected, rejected...), nil
}
