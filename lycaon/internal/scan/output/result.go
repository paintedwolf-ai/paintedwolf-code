package output

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// SecretIdentity stays on the private scanner transport and in host storage.
// FindingIndex follows the result's finding slice, including chunk offsets.
type SecretIdentity struct {
	FindingIndex     int    `json:"finding_index"`
	ValueFingerprint string `json:"value_fingerprint"`
}

// Result is the structured output of a scanner run.
type Result struct {
	SecretIdentities []SecretIdentity `json:"-"`
	ScannedPaths     []string
	FindingsCount    int
	Categories       []api.ScanCategory
	Findings         []api.SecurityFinding
	Warnings         []api.ScanWarning
	Raw              map[string]any
	// ResultSpillPath is host-data-relative when the full body spilled.
	ResultSpillPath string `json:"result_spill_path,omitempty"`
	// ResultSpillBytes is the on-disk body size when spilled.
	ResultSpillBytes int `json:"result_spill_bytes,omitempty"`
}

// CoverageForResult combines engine gaps with the structured source admission facts.
func CoverageForResult(result *Result, sourceQuality, admissionMode string) api.ScanCoverageStatus {
	if result == nil {
		return api.ScanCoverageUnavailable
	}
	if sourceQuality != string(sourcesnapshot.CaptureExact) {
		return api.ScanCoveragePartial
	}
	for _, warning := range result.Warnings {
		switch warning.Kind {
		case api.ScanWarningRuleParseError, api.ScanWarningFilePartialParse, api.ScanWarningFilePartialSemantics,
			api.ScanWarningTargetUnscanned, api.ScanWarningSourceMoved:
			return api.ScanCoveragePartial
		}
	}
	switch sourcesnapshot.AdmissionMode(admissionMode) {
	case sourcesnapshot.AdmissionScopeBounded:
		return api.ScanCoverageBounded
	case sourcesnapshot.AdmissionScope:
		return api.ScanCoverageComplete
	default:
		return api.ScanCoveragePartial
	}
}

// NormalizeResultPaths removes immutable snapshot-tree locations before any
// finding is fingerprinted or persisted. Paths inside scanRoot become stable,
// slash-separated project-relative identities.
func NormalizeResultPaths(result *Result, scanRoot string) {
	if result == nil || strings.TrimSpace(scanRoot) == "" {
		return
	}
	root := filepath.Clean(scanRoot)
	for i := range result.Findings {
		scanfindings.VisitFindingLocations(&result.Findings[i], func(location *api.SecurityFindingLocation) {
			location.URI = normalizeResultPath(root, location.URI)
		})
		scanfindings.RefreshFingerprint(&result.Findings[i])
	}
	for i := range result.ScannedPaths {
		result.ScannedPaths[i] = normalizeResultPath(root, result.ScannedPaths[i])
	}
	for i := range result.Warnings {
		result.Warnings[i].File = normalizeResultPath(root, result.Warnings[i].File)
	}
}

func normalizeResultPath(root, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		if !strings.EqualFold(parsed.Scheme, "file") {
			return raw
		}
		if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
			return raw
		}
		raw = parsed.Path
	}
	clean := filepath.Clean(raw)
	if filepath.IsAbs(clean) {
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return filepath.ToSlash(clean)
		}
		clean = rel
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return filepath.ToSlash(raw)
	}
	return filepath.ToSlash(clean)
}
