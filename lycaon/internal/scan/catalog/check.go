package catalog

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/rules"
)

const (
	ScannerDiagnosticDisabled      = "disabled"
	ScannerDiagnosticRulesInvalid  = "rules_invalid"
	ScannerDiagnosticDriverUnknown = "driver_unknown"
)

// ScannerDiagnostic keeps machine identity separate from operator-facing detail.
type ScannerDiagnostic struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// ScannerCheckReport is the install-check status for one catalog entry.
type ScannerCheckReport struct {
	ID               string              `json:"id"`
	Driver           string              `json:"driver"`
	Categories       []string            `json:"categories"`
	Enabled          bool                `json:"enabled"`
	BinaryFound      bool                `json:"binary_found,omitempty"`
	ParserRegistered bool                `json:"parser_registered,omitempty"`
	CheckOK          bool                `json:"check_ok"`
	Issues           []ScannerDiagnostic `json:"issues,omitempty"`
	ExpandedCommand  []string            `json:"expanded_command,omitempty"`
}

// CheckCatalog evaluates merged scanner entries for CLI/API check output.
func CheckCatalog(cfg *ScannerConfig, moduleRoot, projectDir string) []ScannerCheckReport {
	if cfg == nil {
		return nil
	}
	out := make([]ScannerCheckReport, 0, len(cfg.Scanners))
	for _, entry := range cfg.Scanners {
		out = append(out, checkEntry(entry, moduleRoot, projectDir))
	}
	return out
}

func checkEntry(entry ScannerEntry, moduleRoot, projectDir string) ScannerCheckReport {
	report := ScannerCheckReport{
		ID:         entry.ID,
		Driver:     strings.TrimSpace(entry.Driver),
		Categories: append([]string(nil), entry.Categories...),
		Enabled:    entry.EnabledOrDefault(),
		CheckOK:    true,
	}
	switch report.Driver {
	case DriverExternal:
		report.ParserRegistered = scanoutput.IsRegisteredOutputParser(strings.TrimSpace(entry.OutputParser))
		if !report.ParserRegistered {
			report.CheckOK = false
			report.addIssue(RejectInvalidEntry, "unknown output_parser "+entry.OutputParser)
		}
		report.BinaryFound = BinaryOnPath(entry.Command)
		if !report.BinaryFound {
			if entry.SkipIfBinaryMissingOrDefault() {
				report.addIssue(RejectBinaryMissing, "binary not on PATH (will be skipped at runtime)")
			} else {
				report.CheckOK = false
				report.addIssue(RejectBinaryMissing, "binary not on PATH")
			}
		}
		if argv, err := ExpandCommand(entry.Command, projectDir, projectDir, ""); err == nil {
			report.ExpandedCommand = argv
		}
		if !report.Enabled {
			report.CheckOK = false
			report.addIssue(ScannerDiagnosticDisabled, "disabled")
		}
	case DriverLibrary, DriverBundled:
		report.ParserRegistered = true
		report.BinaryFound = true
		if report.Driver == DriverBundled && entry.Impl == bundled.ImplOpengrep {
			gates, gerr := rules.LoadOpengrepGates()
			if gerr != nil {
				report.CheckOK = false
				report.addIssue(ScannerDiagnosticRulesInvalid, gerr.Error())
			} else if _, err := rules.CompileGateRules(gates, ""); err != nil {
				report.CheckOK = false
				report.addIssue(ScannerDiagnosticRulesInvalid, err.Error())
			}
		}
		if !report.Enabled {
			report.CheckOK = false
			report.addIssue(ScannerDiagnosticDisabled, "disabled")
		}
	default:
		report.CheckOK = false
		report.addIssue(ScannerDiagnosticDriverUnknown, fmt.Sprintf("unknown driver %q", report.Driver))
	}
	return report
}

func (r *ScannerCheckReport) addIssue(code, detail string) {
	r.Issues = append(r.Issues, ScannerDiagnostic{Code: code, Detail: detail})
}

// ListCatalogEntries returns merged catalog metadata for list output.
func ListCatalogEntries(cfg *ScannerConfig, moduleRoot, projectDir string) []ScannerCheckReport {
	reports := CheckCatalog(cfg, moduleRoot, projectDir)
	for i := range reports {
		reports[i].ExpandedCommand = nil
	}
	return reports
}
