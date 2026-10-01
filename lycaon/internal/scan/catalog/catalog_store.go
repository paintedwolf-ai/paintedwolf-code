package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/fseffect"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"gopkg.in/yaml.v3"
)

const scannersFileMode = 0o600

// ErrScannerNotFound names a scanner id no catalog layer provides, or a
// missing user overlay row.
var ErrScannerNotFound = errors.New("scanner not found")

// ErrScannerExists is returned when creating a scanner whose id already exists.
var ErrScannerExists = errors.New("scanner already exists")

// CatalogStore applies transactional scanner overlay changes.
type CatalogStore struct {
	moduleRoot string
	homeDir    string
	publish    func() error
}

// NewCatalogStore returns a scanner catalog writer rooted at one device config.
func NewCatalogStore(moduleRoot, homeDir string, publish func() error) *CatalogStore {
	return &CatalogStore{moduleRoot: moduleRoot, homeDir: homeDir, publish: publish}
}

func (s *CatalogStore) update(ctx context.Context, target string, publish bool, apply func() error) error {
	var publishFn func() error
	if publish {
		publishFn = s.publish
	}
	return catalogruntime.UpdateFile(ctx, catalogruntime.FileUpdate{
		LockRoot: s.homeDir,
		Target:   target,
		Mode:     scannersFileMode,
		DirMode:  0o700,
		Apply:    apply,
		Publish:  publishFn,
	})
}

// CatalogStoreError is a validation/trust error with a machine code for HTTP mapping.
type CatalogStoreError struct {
	Code    string
	Message string
}

func (e *CatalogStoreError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

func storeErr(code, message string) error {
	return &CatalogStoreError{Code: code, Message: message}
}

// LoadUserScannerOverlay reads a user or project scanners.yaml (missing → empty).
func LoadUserScannerOverlay(path string) (*ScannerConfig, error) {
	cfg, err := loadOptionalScannerConfig(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &ScannerConfig{}, nil
	}
	return cfg, nil
}

func saveUserScannerOverlay(path string, cfg *ScannerConfig) error {
	if cfg == nil {
		cfg = &ScannerConfig{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(cfg.Scanners) == 0 {
		if err := fseffect.Remove(fseffect.RemoveRequest{Location: fseffect.PathLocation(path)}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     scannersFileMode,
		DirMode:  0o700,
	})
	return err
}

func upsertUserScannerRow(cfg *ScannerConfig, entry ScannerEntry) {
	for i, s := range cfg.Scanners {
		if s.ID == entry.ID {
			cfg.Scanners[i] = entry
			return
		}
	}
	cfg.Scanners = append(cfg.Scanners, entry)
}

// SaveDeviceScannerEntry writes a scanner and selects it when enabled.
// Nil preserves an existing state; new rows start disabled.
func (s *CatalogStore) SaveDeviceScannerEntry(ctx context.Context, entry ScannerEntry) error {
	entry.ID = strings.TrimSpace(entry.ID)
	if err := validateUserExternalScanner(s.homeDir, entry); err != nil {
		return err
	}
	slot := PrimaryCategory(entry.Categories)
	if !IsSlotCategory(slot) {
		return storeErr(RejectInvalidEntry, fmt.Sprintf("scanner %q maps to no scanner slot", entry.ID))
	}
	path := UserScannersPath(s.homeDir)
	return s.update(ctx, path, true, func() error {
		overlay, err := LoadUserScannerOverlay(path)
		if err != nil {
			return err
		}
		merged, _, err := LoadMergedScannerCatalog(s.moduleRoot, "", s.homeDir)
		if err != nil {
			return err
		}
		if entry.Enabled == nil {
			enabled := false
			if current, ok := findScannerEntry(merged, entry.ID); ok {
				enabled = current.EnabledOrDefault()
			}
			entry.Enabled = &enabled
		}
		upsertUserScannerRow(overlay, entry)
		if entry.EnabledOrDefault() {
			candidates := append([]ScannerEntry(nil), merged.Scanners...)
			replaced := false
			for i := range candidates {
				if candidates[i].ID == entry.ID {
					candidates[i] = entry
					replaced = true
					break
				}
			}
			if !replaced {
				candidates = append(candidates, entry)
			}
			if err := selectScannerInOverlay(overlay, candidates, slot, entry.ID); err != nil {
				return err
			}
		}
		return saveUserScannerOverlay(path, overlay)
	})
}

// DeleteUserScannerRow removes a user overlay row. Returns ErrScannerNotFound if absent.
func (s *CatalogStore) DeleteUserScannerRow(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	path := UserScannersPath(s.homeDir)
	return s.update(ctx, path, true, func() error {
		cfg, err := LoadUserScannerOverlay(path)
		if err != nil {
			return err
		}
		next := make([]ScannerEntry, 0, len(cfg.Scanners))
		found := false
		for _, row := range cfg.Scanners {
			if row.ID == id {
				found = true
				continue
			}
			next = append(next, row)
		}
		if !found {
			return ErrScannerNotFound
		}
		cfg.Scanners = next
		return saveUserScannerOverlay(path, cfg)
	})
}

// SetProjectScannerEnabled writes project enable-only overlay for a known scanner id.
func (s *CatalogStore) SetProjectScannerEnabled(ctx context.Context, projectDir, id string, enabled bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return storeErr(RejectInvalidEntry, "scanner id required")
	}
	path := ProjectScannersPath(projectDir)
	return s.update(ctx, path, false, func() error {
		merged, _, err := LoadMergedScannerCatalog(s.moduleRoot, "", s.homeDir)
		if err != nil {
			return err
		}
		chosen, known := findScannerEntry(merged, id)
		if !known {
			return fmt.Errorf("%w: %q", ErrScannerNotFound, id)
		}
		cfg, err := LoadUserScannerOverlay(path)
		if err != nil {
			return err
		}
		for _, row := range cfg.Scanners {
			if !isEnableOnlyRow(row) {
				return storeErr(RejectProjectFieldsForbidden, "project scanners.yaml may only set id and enabled")
			}
		}
		if enabled {
			slot := PrimaryCategory(chosen.Categories)
			for _, scanner := range merged.Scanners {
				if PrimaryCategory(scanner.Categories) != slot {
					continue
				}
				if err := setScannerEnabledInOverlay(cfg, scanner.ID, scanner.ID == id, true); err != nil {
					return err
				}
			}
		} else if err := setScannerEnabledInOverlay(cfg, id, false, true); err != nil {
			return err
		}
		return saveUserScannerOverlay(path, cfg)
	})
}

// setScannerEnabledInOverlay updates an in-memory overlay so callers can
// persist a complete slot swap with one file replacement.
func setScannerEnabledInOverlay(cfg *ScannerConfig, id string, enabled, projectOnly bool) error {
	value := enabled
	for i, row := range cfg.Scanners {
		if row.ID != id {
			continue
		}
		if projectOnly && !isEnableOnlyRow(row) {
			return storeErr(RejectProjectFieldsForbidden, "project scanners.yaml may only set id and enabled")
		}
		if projectOnly || isEnableOnlyRow(row) {
			cfg.Scanners[i] = ScannerEntry{ID: id, Enabled: &value}
		} else {
			row.Enabled = &value
			cfg.Scanners[i] = row
		}
		return nil
	}
	cfg.Scanners = append(cfg.Scanners, ScannerEntry{ID: id, Enabled: &value})
	return nil
}

// CreateUserExternalScanner validates and persists a new user external scanner.
func (s *CatalogStore) CreateUserExternalScanner(ctx context.Context, entry ScannerEntry) error {
	id := strings.TrimSpace(entry.ID)
	if id == "" {
		return storeErr(RejectInvalidEntry, "scanner id required")
	}
	entry.ID = id
	entry.Driver = DriverExternal
	entry.Impl = ""
	off := false
	entry.Enabled = &off

	host, err := LoadScannerConfig()
	if err != nil {
		return err
	}
	for _, s := range host.Scanners {
		if s.ID == id {
			return storeErr(RejectInvalidEntry, fmt.Sprintf("scanner id %q is host stock; use PUT to overlay", id))
		}
	}

	if err := validateUserExternalScanner(s.homeDir, entry); err != nil {
		return err
	}
	path := UserScannersPath(s.homeDir)
	return s.update(ctx, path, true, func() error {
		existing, err := LoadUserScannerOverlay(path)
		if err != nil {
			return err
		}
		for _, row := range existing.Scanners {
			if row.ID == id {
				return ErrScannerExists
			}
		}
		upsertUserScannerRow(existing, entry)
		return saveUserScannerOverlay(path, existing)
	})
}

func validateUserExternalScanner(homeDir string, entry ScannerEntry) error {
	if entry.Driver != DriverExternal || strings.TrimSpace(entry.Impl) != "" {
		return storeErr(RejectCommunityDriverForbidden, "user scanner definitions must use the external driver")
	}
	if err := validateScannerEntry(entry); err != nil {
		return storeErr(RejectInvalidEntry, err.Error())
	}
	if !scanoutput.IsUserExternalParserAllowed(strings.TrimSpace(entry.OutputParser)) {
		return storeErr(RejectCommunityParserForbidden,
			fmt.Sprintf("output_parser %q not allowed for user external scanners", entry.OutputParser))
	}
	if err := ValidateExternalArgv(entry.Command, nil); err != nil {
		return storeErr(catalogErrorCode(err), catalogErrorDetail(err))
	}
	if strings.TrimSpace(entry.OutputParser) == scanoutput.OutputParserMapJSON {
		if _, err := scanoutput.LoadMapper(homeDir, strings.TrimSpace(entry.MapperID)); err != nil {
			return storeErr(RejectMapperMissing, err.Error())
		}
	}
	return nil
}

// CommandSummary joins argv without expanding placeholders or including env values.
func CommandSummary(command []string) string {
	if len(command) == 0 {
		return ""
	}
	return strings.Join(command, " ")
}

// RequiresBinaryLabel returns a display hint for the external binary (basename of argv0).
func RequiresBinaryLabel(command []string) string {
	if len(command) == 0 {
		return ""
	}
	return filepath.Base(strings.TrimSpace(command[0]))
}

// CheckIssueCodes projects typed check diagnostics to wire codes.
func CheckIssueCodes(report ScannerCheckReport) []string {
	out := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		if issue.Code == ScannerDiagnosticDisabled {
			// Not a trust rejection; omit from closed issue codes.
			continue
		}
		if issue.Code != "" {
			out = append(out, issue.Code)
		}
	}
	return out
}
