package findings

import (
	"strings"

	"github.com/lycaon/lycaon/internal/advisory/severity"
	"github.com/lycaon/lycaon/pkg/api"
)

// FindingBuildOpts configures BuildSecurityFinding.
type FindingBuildOpts struct {
	DriverID    string
	ToolName    string
	ToolVersion string
	RuleID      string
	Level       api.FindingLevel
	Message     string
	Kind        api.FindingKind
	Locations   []api.SecurityFindingLocation
	Advisory    *api.AdvisoryRef
	Categories  []api.ScanCategory
}

// BuildSecurityFinding constructs a parser row with fingerprint and lycaon properties.
func BuildSecurityFinding(opts FindingBuildOpts) api.SecurityFinding {
	driverID := strings.TrimSpace(opts.DriverID)
	if driverID == "" {
		driverID = "unknown"
	}
	toolName := strings.TrimSpace(opts.ToolName)
	if toolName == "" {
		toolName = driverID
	}
	kind := opts.Kind
	if kind == "" {
		kind = api.FindingKindCustom
	}
	locs := opts.Locations
	if len(locs) == 0 {
		locs = []api.SecurityFindingLocation{{URI: ""}}
	}
	adv := opts.Advisory
	if adv == nil {
		adv = RuleAdvisory(opts.RuleID)
	}
	osvID := ""
	if adv != nil {
		osvID = adv.OSVID
	}
	uri := strings.TrimSpace(locs[0].URI)
	startLine := locs[0].StartLine
	props := &api.SecurityFindingProperties{
		Lycaon: &api.SecurityFindingLycaonProperties{
			Kind:       kind,
			Categories: opts.Categories,
			Advisory:   adv,
		},
	}
	finding := api.SecurityFinding{
		RuleID:    strings.TrimSpace(opts.RuleID),
		Level:     opts.Level,
		Message:   strings.TrimSpace(opts.Message),
		Locations: locs,
		Fingerprints: api.SecurityFindingFingerprints{
			Primary: PrimaryFingerprint(driverID, kind, strings.TrimSpace(opts.RuleID), uri, startLine, osvID),
		},
		Tool: api.ToolDescriptor{
			DriverID: driverID,
			Name:     toolName,
			Version:  strings.TrimSpace(opts.ToolVersion),
		},
		Properties: props,
	}
	// Enrichment rates the finding; its identity is fixed above from scanner-reported fields.
	if resolver, err := severity.Default(); err == nil {
		EnrichSecurityFinding(&finding, resolver)
	}
	return finding
}

// FindingKind returns properties.lycaon.kind or custom when unset.
func FindingKind(f api.SecurityFinding) api.FindingKind {
	if f.Properties != nil && f.Properties.Lycaon != nil && f.Properties.Lycaon.Kind != "" {
		return f.Properties.Lycaon.Kind
	}
	return api.FindingKindCustom
}

// SetHintCode stamps properties.lycaon.hint_code on a finding row.
func SetHintCode(f api.SecurityFinding, code string) api.SecurityFinding {
	code = strings.TrimSpace(code)
	if code == "" {
		return f
	}
	if f.Properties == nil {
		f.Properties = &api.SecurityFindingProperties{Lycaon: &api.SecurityFindingLycaonProperties{}}
	}
	if f.Properties.Lycaon == nil {
		f.Properties.Lycaon = &api.SecurityFindingLycaonProperties{}
	}
	f.Properties.Lycaon.HintCode = code
	return f
}

// FixtureFinding builds a SecurityFinding for tests.
func FixtureFinding(ruleID string, level api.FindingLevel, message, uri string, line int) api.SecurityFinding {
	return BuildSecurityFinding(FindingBuildOpts{
		DriverID: "fixture",
		RuleID:   ruleID,
		Level:    level,
		Message:  message,
		Kind:     api.FindingKindSAST,
		Locations: []api.SecurityFindingLocation{{
			URI:       uri,
			StartLine: line,
		}},
	})
}

// NormalizeSARIFLevel maps SARIF result.level to FindingLevel.
func NormalizeSARIFLevel(level string) api.FindingLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error":
		return api.FindingLevelHigh
	case "warning":
		return api.FindingLevelMedium
	case "note":
		return api.FindingLevelLow
	case "none":
		return api.FindingLevelInfo
	default:
		return api.FindingLevelMedium
	}
}

// NormalizeVendorSeverity maps vendor severity strings to FindingLevel.
func NormalizeVendorSeverity(severity string) api.FindingLevel {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case "CRITICAL":
		return api.FindingLevelCritical
	case "HIGH", "ERROR":
		return api.FindingLevelHigh
	case "MEDIUM", "WARNING", "WARN":
		return api.FindingLevelMedium
	case "LOW", "NOTE":
		return api.FindingLevelLow
	default:
		return api.FindingLevelInfo
	}
}

// LevelToGuidanceSeverity maps FindingLevel to the compact severity used by scan guidance.
func LevelToGuidanceSeverity(level api.FindingLevel) string {
	switch level {
	case api.FindingLevelCritical, api.FindingLevelHigh:
		return "error"
	case api.FindingLevelMedium:
		return "warning"
	default:
		return "info"
	}
}

// MinSeverityRank orders the configured minimum finding severity.
func MinSeverityRank(min string) int {
	switch strings.ToLower(strings.TrimSpace(min)) {
	case "critical":
		return 0
	case "error", "high":
		return 1
	case "warning", "warn", "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}
