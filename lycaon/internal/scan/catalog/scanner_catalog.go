package catalog

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/catalogruntime"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
)

// ArgTokenReportPath requests a host-managed report file.
const ArgTokenReportPath = "{{report" + "_path}}"

// ArgTokenProjectDir expands to the published project root.
const ArgTokenProjectDir = "{{project" + "_dir}}"

// ArgTokenScanTarget expands to the exact host-selected path for one invocation.
const ArgTokenScanTarget = "{{scan" + "_target}}"

// ScannerDefinition is one scanner-catalog row.
type ScannerDefinition struct {
	ID           string   `yaml:"id"`
	Label        string   `yaml:"label"`
	Binary       string   `yaml:"binary"`
	Engine       string   `yaml:"engine"`
	ScopeKind    string   `yaml:"scope_kind"`
	Categories   []string `yaml:"categories"`
	Command      []string `yaml:"command"`
	OutputParser string   `yaml:"output_parser"`
	// Probe is a cheap liveness command (usually --version) run by install check.
	Probe []string `yaml:"probe"`
	Env   []string `yaml:"env"`
	// OkExitCodes is the tool's success contract; see ScannerEntry.ExitCodeAllowed.
	OkExitCodes []int `yaml:"ok_exit_codes"`
	// Install is empty when the catalog has no supported install command.
	Install string `yaml:"install"`
	DocsURL string `yaml:"docs_url"`
	Hint    string `yaml:"hint"`
}

// CommandWantsReportFile reports whether argv asks the host for a temp report
// path instead of reading the tool's stdout.
func CommandWantsReportFile(command []string) bool {
	for _, arg := range command {
		if strings.Contains(arg, ArgTokenReportPath) {
			return true
		}
	}
	return false
}

// CommandUsesScanTarget reports whether argv consumes the host-selected path.
func CommandUsesScanTarget(command []string) bool {
	for _, arg := range command {
		if strings.Contains(arg, ArgTokenScanTarget) {
			return true
		}
	}
	return false
}

// ScannerCatalog is the bundled known-scanner list.
type ScannerCatalog struct {
	entries *catalogruntime.Catalog[ScannerDefinition]
}

type scannerCatalogFile struct {
	Scanners []ScannerDefinition `yaml:"scanners"`
}

// LoadScannerCatalog reads and validates scanner-catalog.yaml.
func LoadScannerCatalog() (*ScannerCatalog, error) {
	data, err := config.Read(config.ScannerCatalog)
	if err != nil {
		return nil, fmt.Errorf("read scanner catalog: %w", err)
	}
	var raw scannerCatalogFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, fmt.Errorf("parse scanner catalog: %w", err)
	}
	if len(raw.Scanners) == 0 {
		return nil, fmt.Errorf("scanner catalog: no scanners")
	}
	items := make([]catalogruntime.Item[ScannerDefinition], 0, len(raw.Scanners))
	for _, entry := range raw.Scanners {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			return nil, fmt.Errorf("scanner catalog: entry missing id")
		}
		if err := validateScannerDefinition(id, entry); err != nil {
			return nil, err
		}
		entry.ID = id
		items = append(items, catalogruntime.Item[ScannerDefinition]{
			ID: id, Spec: entry,
		})
	}
	entries, err := catalogruntime.Assemble([]catalogruntime.Layer[ScannerDefinition]{
		{Name: "bundled scanner catalog", Items: items},
	}, func(existing catalogruntime.Item[ScannerDefinition], exists bool, incoming catalogruntime.Item[ScannerDefinition]) (catalogruntime.Item[ScannerDefinition], error) {
		if !exists {
			return incoming, nil
		}
		return existing, fmt.Errorf("scanner catalog: duplicate id %q", incoming.ID)
	})
	if err != nil {
		return nil, err
	}
	return &ScannerCatalog{entries: entries}, nil
}

func validateScannerDefinition(id string, entry ScannerDefinition) error {
	if strings.TrimSpace(entry.Label) == "" {
		return fmt.Errorf("scanner catalog: %q missing label", id)
	}
	if strings.TrimSpace(entry.Binary) == "" {
		return fmt.Errorf("scanner catalog: %q missing binary", id)
	}
	if strings.TrimSpace(entry.Engine) == "" {
		return fmt.Errorf("scanner catalog: %q missing engine", id)
	}
	if !validScopeKind(ScopeKind(strings.TrimSpace(entry.ScopeKind))) {
		return fmt.Errorf("scanner catalog: %q invalid scope_kind %q", id, entry.ScopeKind)
	}
	if len(entry.Categories) == 0 {
		return fmt.Errorf("scanner catalog: %q requires at least one category", id)
	}
	// Every row must map to a selectable scanner slot.
	primary := PrimaryCategory(entry.Categories)
	if !IsSlotCategory(primary) {
		return fmt.Errorf("scanner catalog: %q primary category %q maps to no scanner slot", id, primary)
	}
	if len(entry.Command) == 0 {
		return fmt.Errorf("scanner catalog: %q requires a command", id)
	}
	if strings.TrimSpace(entry.Command[0]) != strings.TrimSpace(entry.Binary) {
		return fmt.Errorf("scanner catalog: %q command[0] %q must be the declared binary %q",
			id, entry.Command[0], entry.Binary)
	}
	// Catalog rows use the user-layer parser allowlist.
	if !scanoutput.IsUserExternalParserAllowed(strings.TrimSpace(entry.OutputParser)) {
		return fmt.Errorf("scanner catalog: %q output_parser %q is not allowed for user-layer scanners",
			id, entry.OutputParser)
	}
	if err := ValidateExternalArgv(entry.Command, nil); err != nil {
		return fmt.Errorf("scanner catalog: %q: %w", id, err)
	}
	if !CommandUsesScanTarget(entry.Command) {
		return fmt.Errorf("scanner catalog: %q external command must contain %s", id, ArgTokenScanTarget)
	}
	if len(entry.Probe) > 0 && strings.TrimSpace(entry.Probe[0]) != strings.TrimSpace(entry.Binary) {
		return fmt.Errorf("scanner catalog: %q probe[0] %q must be the declared binary %q",
			id, entry.Probe[0], entry.Binary)
	}
	for _, code := range entry.OkExitCodes {
		if code < 0 {
			return fmt.Errorf("scanner catalog: %q ok_exit_codes must be non-negative, got %d", id, code)
		}
	}
	return nil
}

// Entry returns one catalog row by wire id.
func (c *ScannerCatalog) Entry(id string) (ScannerDefinition, bool) {
	if c == nil {
		return ScannerDefinition{}, false
	}
	item, ok := c.entries.Get(id)
	return cloneScannerDefinition(item.Spec), ok
}

// Entries returns isolated catalog rows in file order.
func (c *ScannerCatalog) Entries() []ScannerDefinition {
	if c == nil {
		return nil
	}
	items := c.entries.Items()
	out := make([]ScannerDefinition, len(items))
	for i, item := range items {
		out[i] = cloneScannerDefinition(item.Spec)
	}
	return out
}

func cloneScannerDefinition(entry ScannerDefinition) ScannerDefinition {
	entry.Categories = append([]string(nil), entry.Categories...)
	entry.Command = append([]string(nil), entry.Command...)
	entry.Probe = append([]string(nil), entry.Probe...)
	entry.Env = append([]string(nil), entry.Env...)
	entry.OkExitCodes = append([]int(nil), entry.OkExitCodes...)
	return entry
}

// ScannerEntryFor converts a catalog row into a disabled user scanner entry.
func (c *ScannerCatalog) ScannerEntryFor(id string) (ScannerEntry, bool) {
	entry, ok := c.Entry(id)
	if !ok {
		return ScannerEntry{}, false
	}
	off := false
	return ScannerEntry{
		ID:           entry.ID,
		Driver:       DriverExternal,
		Engine:       entry.Engine,
		ScopeKind:    entry.ScopeKind,
		Categories:   append([]string(nil), entry.Categories...),
		Command:      append([]string(nil), entry.Command...),
		OutputParser: entry.OutputParser,
		Env:          append([]string(nil), entry.Env...),
		OkExitCodes:  append([]int(nil), entry.OkExitCodes...),
		Label:        entry.Label,
		Description:  entry.Hint,
		Enabled:      &off,
	}, true
}
