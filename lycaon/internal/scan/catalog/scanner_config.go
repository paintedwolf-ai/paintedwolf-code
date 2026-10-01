package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/config"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	DriverLibrary  = "library"
	DriverBundled  = "bundled"
	DriverExternal = "external"
)

const (
	WorkdirProject    = "project"
	WorkdirModuleRoot = "module_root"
)

const (
	defaultScanSoftLimitSec = 15 * 60
	defaultScanCPUUnits     = 1
	defaultScanParallelism  = 1
)

// RuntimePolicy defines resource and execution limits for one scanner.
// Soft limits report delay; an optional hard limit cancels execution.
type RuntimePolicy struct {
	SoftLimitSec int `yaml:"soft_limit_sec" json:"soft_limit_sec"`
	HardLimitSec int `yaml:"hard_limit_sec" json:"hard_limit_sec"`
	CPUUnits     int `yaml:"cpu_units" json:"cpu_units"`
	Parallelism  int `yaml:"parallelism" json:"parallelism"`
}

// Normalized applies defaults to omitted policy fields.
func (p RuntimePolicy) Normalized() RuntimePolicy {
	if p.SoftLimitSec <= 0 {
		p.SoftLimitSec = defaultScanSoftLimitSec
	}
	if p.CPUUnits <= 0 {
		p.CPUUnits = defaultScanCPUUnits
	}
	if p.Parallelism <= 0 {
		p.Parallelism = defaultScanParallelism
	}
	return p
}

// Context inherits owner cancellation and any explicitly selected runtime ceiling.
func (p RuntimePolicy) Context(parent context.Context) (context.Context, context.CancelFunc) {
	if p.HardLimitSec == 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(p.HardLimitSec)*time.Second)
}

// ValidateRuntimePolicy checks an explicit, fully populated runtime policy.
func ValidateRuntimePolicy(policy RuntimePolicy) error {
	if policy.SoftLimitSec <= 0 {
		return fmt.Errorf("soft_limit_sec must be positive")
	}
	if policy.HardLimitSec < 0 || (policy.HardLimitSec > 0 && policy.HardLimitSec < policy.SoftLimitSec) {
		return fmt.Errorf("hard_limit_sec must be zero (no deadline) or >= soft_limit_sec")
	}
	if policy.CPUUnits < 1 || policy.CPUUnits > 8 {
		return fmt.Errorf("cpu_units must be between 1 and 8")
	}
	if policy.Parallelism < 1 || policy.Parallelism > 16 {
		return fmt.Errorf("parallelism must be between 1 and 16")
	}
	return nil
}

// ScannerConfig is the on-disk scanners.yaml shape.
type ScannerConfig struct {
	Scanners []ScannerEntry `yaml:"scanners"`
}

// ScannerEntry describes one scanner in the driver catalog.
type ScannerEntry struct {
	ID           string   `yaml:"id"`
	Driver       string   `yaml:"driver,omitempty"`
	Impl         string   `yaml:"impl,omitempty"`
	Engine       string   `yaml:"engine,omitempty"`
	ScopeKind    string   `yaml:"scope_kind,omitempty"`
	Categories   []string `yaml:"categories"`
	Config       string   `yaml:"config,omitempty"`
	Enabled      *bool    `yaml:"enabled,omitempty"`
	Command      []string `yaml:"command,omitempty"`
	OutputParser string   `yaml:"output_parser,omitempty"`
	// MapperID selects a device mapper for the map/json parser.
	MapperID            string        `yaml:"mapper_id,omitempty"`
	Workdir             string        `yaml:"workdir,omitempty"`
	Runtime             RuntimePolicy `yaml:"runtime"`
	SkipIfBinaryMissing *bool         `yaml:"skip_if_binary_missing,omitempty"`
	Env                 []string      `yaml:"env,omitempty"`
	Label               string        `yaml:"label,omitempty"`
	Description         string        `yaml:"description,omitempty"`
	// OkExitCodes lists completed-run exit codes. Empty accepts every code.
	OkExitCodes []int `yaml:"ok_exit_codes,omitempty"`
	// CatalogSource records the merged layer.
	CatalogSource CatalogSource `yaml:"-" json:"catalog_source,omitempty"`
}

// EnabledOrDefault returns true when enabled is nil or explicitly true.
func (e ScannerEntry) EnabledOrDefault() bool {
	if e.Enabled == nil {
		return true
	}
	return *e.Enabled
}

// SkipIfBinaryMissingOrDefault returns true when skip_if_binary_missing is nil or explicitly true.
func (e ScannerEntry) SkipIfBinaryMissingOrDefault() bool {
	if e.SkipIfBinaryMissing == nil {
		return true
	}
	return *e.SkipIfBinaryMissing
}

// ExitCodeAllowed reports whether an exit code denotes a completed run.
func (e ScannerEntry) ExitCodeAllowed(code int) bool {
	if len(e.OkExitCodes) == 0 {
		return true
	}
	for _, ok := range e.OkExitCodes {
		if ok == code {
			return true
		}
	}
	return false
}

// WorkdirOrDefault returns project when workdir is empty.
func (e ScannerEntry) WorkdirOrDefault() string {
	if w := strings.TrimSpace(e.Workdir); w != "" {
		return w
	}
	return WorkdirProject
}

// RuntimePolicy returns the normalized execution policy for this scanner.
func (e ScannerEntry) RuntimePolicy() RuntimePolicy { return e.Runtime.Normalized() }

// CategoriesAPI returns categories as api.ScanCategory values.
func (e ScannerEntry) CategoriesAPI() []api.ScanCategory {
	out := make([]api.ScanCategory, 0, len(e.Categories))
	for _, c := range e.Categories {
		out = append(out, api.ScanCategory(c))
	}
	return out
}

// LoadScannerConfig reads the bundled host catalog layer.
func LoadScannerConfig() (*ScannerConfig, error) {
	data, err := config.Read(config.ScannersList)
	if err != nil {
		return nil, err
	}
	var cfg ScannerConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ValidateScannerConfig checks the scanner catalog shape.
func ValidateScannerConfig(cfg *ScannerConfig) error {
	if cfg == nil {
		return fmt.Errorf("scanner config is nil")
	}
	if len(cfg.Scanners) == 0 {
		return fmt.Errorf("scanners: at least one entry required")
	}
	seen := make(map[string]struct{}, len(cfg.Scanners))
	for _, s := range cfg.Scanners {
		if err := validateScannerEntry(s); err != nil {
			return err
		}
		if _, dup := seen[s.ID]; dup {
			return fmt.Errorf("duplicate scanner id %q", s.ID)
		}
		seen[s.ID] = struct{}{}
	}
	if err := validateScannerSlotSelection(cfg.Scanners); err != nil {
		return err
	}
	return nil
}

func validateScannerEntry(s ScannerEntry) error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("scanner missing id")
	}
	if len(s.Categories) == 0 {
		return fmt.Errorf("scanner %q: categories required", s.ID)
	}
	for _, c := range s.Categories {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("scanner %q: empty category", s.ID)
		}
	}
	primary := PrimaryCategory(s.Categories)
	if !IsSlotCategory(primary) {
		return fmt.Errorf("scanner %q: primary category %q maps to no scanner slot", s.ID, primary)
	}
	for _, code := range s.OkExitCodes {
		if code < 0 {
			return fmt.Errorf("scanner %q: ok_exit_codes must be non-negative, got %d", s.ID, code)
		}
	}
	runtime := s.Runtime.Normalized()
	if err := ValidateRuntimePolicy(runtime); err != nil {
		return fmt.Errorf("scanner %q runtime: %w", s.ID, err)
	}
	if strings.TrimSpace(s.Engine) == "" {
		return fmt.Errorf("scanner %q: engine required", s.ID)
	}
	if !validScopeKind(ScopeKind(strings.TrimSpace(s.ScopeKind))) {
		return fmt.Errorf("scanner %q: invalid scope_kind %q", s.ID, s.ScopeKind)
	}

	driver := strings.TrimSpace(s.Driver)
	if driver == "" {
		return fmt.Errorf("scanner %q: driver required", s.ID)
	}
	switch driver {
	case DriverLibrary, DriverBundled:
		if strings.TrimSpace(s.Impl) == "" {
			return fmt.Errorf("scanner %q: impl required for driver %q", s.ID, driver)
		}
		if driver == DriverBundled && strings.TrimSpace(s.Config) == "" {
			return fmt.Errorf("scanner %q: config required for bundled driver", s.ID)
		}
		return nil
	case DriverExternal:
		if len(s.Command) == 0 {
			return fmt.Errorf("scanner %q: command required for external driver", s.ID)
		}
		if err := validateExternalCommand(s.ID, s.Command); err != nil {
			return err
		}
		if !CommandUsesScanTarget(s.Command) {
			return fmt.Errorf("scanner %q: external command must contain %s", s.ID, ArgTokenScanTarget)
		}
		parser := strings.TrimSpace(s.OutputParser)
		if parser == "" {
			return fmt.Errorf("scanner %q: output_parser required for external driver", s.ID)
		}
		if !scanoutput.IsRegisteredOutputParser(parser) {
			return fmt.Errorf("scanner %q: unknown output_parser %q", s.ID, parser)
		}
		mapperID := strings.TrimSpace(s.MapperID)
		if parser == scanoutput.OutputParserMapJSON {
			if mapperID == "" {
				return fmt.Errorf("scanner %q: mapper_id required for map/json parser", s.ID)
			}
			if !scanoutput.IsValidMapperID(mapperID) {
				return fmt.Errorf("scanner %q: invalid mapper_id %q", s.ID, mapperID)
			}
		} else if mapperID != "" {
			return fmt.Errorf("scanner %q: mapper_id only valid with map/json parser", s.ID)
		}
		switch s.WorkdirOrDefault() {
		case WorkdirProject, WorkdirModuleRoot:
		default:
			return fmt.Errorf("scanner %q: unknown workdir %q", s.ID, s.Workdir)
		}
		return nil
	default:
		return fmt.Errorf("scanner %q: unknown driver %q", s.ID, driver)
	}
}

func validateScannerSlotSelection(scanners []ScannerEntry) error {
	selected := make(map[string]string, len(SlotCategories()))
	for _, scanner := range scanners {
		if !scanner.EnabledOrDefault() {
			continue
		}
		slot := PrimaryCategory(scanner.Categories)
		if previous, exists := selected[slot]; exists {
			return fmt.Errorf("scanner slot %q has multiple enabled scanners %q and %q", slot, previous, scanner.ID)
		}
		selected[slot] = scanner.ID
	}
	return nil
}

func validateExternalCommand(scannerID string, command []string) error {
	for i, arg := range command {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("scanner %q: empty command argument at index %d", scannerID, i)
		}
		if containsShellMetachars(arg) {
			return fmt.Errorf("scanner %q: command argument %q contains shell metacharacters", scannerID, arg)
		}
	}
	return nil
}

func containsShellMetachars(s string) bool {
	return strings.ContainsAny(s, ";|&$`")
}
