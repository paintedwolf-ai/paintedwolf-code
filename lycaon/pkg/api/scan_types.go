package api

import (
	"encoding/json"
	"strings"
	"time"
)

// --- Extensibility: code scanning ---
// ScanCategory / Finding* consts live in *_ids.generated.go.

// IsKnownScanCategory reports whether c is a registered scan category (not "all").
func IsKnownScanCategory(c ScanCategory) bool {
	for _, known := range AllScanCategoryValues() {
		if c == known {
			return true
		}
	}
	return false
}

// ScanCategoryList returns comma-separated valid category names for error messages.
func ScanCategoryList() string {
	cats := AllScanCategoryValues()
	parts := make([]string, len(cats))
	for i, c := range cats {
		parts[i] = string(c)
	}
	return strings.Join(parts, ", ")
}

type CodeScanStatus string

const (
	CodeScanStatusPending    CodeScanStatus = "pending"
	CodeScanStatusRunning    CodeScanStatus = "running"
	CodeScanStatusComplete   CodeScanStatus = "complete"
	CodeScanStatusFailed     CodeScanStatus = "failed"
	CodeScanStatusTimedOut   CodeScanStatus = "timed_out"
	CodeScanStatusCanceled   CodeScanStatus = "canceled"
	CodeScanStatusSuperseded CodeScanStatus = "superseded"
)

// ScanCoverageStatus describes observed source coverage independently of process completion.
// Bounded coverage includes all admitted files but leaves budget-excluded directories unobserved.
type ScanCoverageStatus string

const (
	ScanCoverageComplete    ScanCoverageStatus = "complete"
	ScanCoverageBounded     ScanCoverageStatus = "bounded"
	ScanCoveragePartial     ScanCoverageStatus = "partial"
	ScanCoverageUnavailable ScanCoverageStatus = "unavailable"
)

// ScanTargetKind distinguishes full source runs from incremental updates, including empty ones.
type ScanTargetKind string

const (
	ScanTargetFull  ScanTargetKind = "full"
	ScanTargetPaths ScanTargetKind = "paths"
)

// FullPassMemberPhase is how far one scanner of a full pass has come.
type FullPassMemberPhase string

const (
	FullPassMemberWaitingForScanner FullPassMemberPhase = "waiting_for_scanner"
	FullPassMemberWaitingForPass    FullPassMemberPhase = "waiting_for_pass"
	FullPassMemberStarted           FullPassMemberPhase = "started"
	FullPassMemberNotStarted        FullPassMemberPhase = "not_started"
)

// ScanFingerprintScheme names the finding identity algorithm a scan was
// fingerprinted under. Comparison refuses scans fingerprinted under different
// schemes.
const ScanFingerprintScheme = "security-finding-v1"

// SourceSnapshotWarming marks scans awaiting immutable input.
const SourceSnapshotWarming = "warming"

// ScanTaskID returns the namespaced task identity for a scan.
func ScanTaskID(scanID string) string {
	return "scan:" + strings.TrimSpace(scanID)
}

type ScanTrigger string

const (
	ScanTriggerLandedChange     ScanTrigger = "landed_change"
	ScanTriggerPhaseEnter       ScanTrigger = "phase_enter"
	ScanTriggerManual           ScanTrigger = "manual"
	ScanTriggerScanPack         ScanTrigger = "scan_pack"
	ScanTriggerWriteBurst       ScanTrigger = "write_burst"
	ScanTriggerAuthorityRefresh ScanTrigger = "authority_refresh"
)

// FindingLevelRank returns lower values for more severe normalized levels.
func FindingLevelRank(level FindingLevel) int {
	switch level {
	case FindingLevelCritical:
		return 0
	case FindingLevelHigh:
		return 1
	case FindingLevelMedium:
		return 2
	case FindingLevelLow:
		return 3
	case FindingLevelUnknown:
		return 5
	default:
		return 4
	}
}

// FindingLevelAtOrAbove reports whether level meets the configured gate floor.
func FindingLevelAtOrAbove(level, floor FindingLevel) bool {
	if floor == "" {
		floor = FindingLevelHigh
	}
	return FindingLevelRank(level) <= FindingLevelRank(floor)
}

// ParseFindingLevel normalizes gate args to the wire enum.
func ParseFindingLevel(raw string) FindingLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical", "crit":
		return FindingLevelCritical
	case "high":
		return FindingLevelHigh
	case "medium", "med", "warning", "warn":
		return FindingLevelMedium
	case "low":
		return FindingLevelLow
	case "info":
		return FindingLevelInfo
	default:
		return FindingLevelHigh
	}
}

// DefaultFindingLevelBucketKeys returns histogram keys for compare response maps.
// Levels, including unknown, come from the wire enum.
func DefaultFindingLevelBucketKeys() []string {
	levels := AllFindingLevelValues()
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		out = append(out, string(level))
	}
	return out
}

// DefaultSupportedQueryFilters lists native scan_query filters.
func DefaultSupportedQueryFilters() []string {
	return []string{
		"level", "kind", "code", "path", "rule_id", "fingerprint", "advisory_id",
	}
}

// ScanWarningKind classifies non-fatal scanner issues surfaced to agents.
type ScanWarningKind string

const (
	ScanWarningRuleParseError       ScanWarningKind = "rule_parse_error"
	ScanWarningFilePartialParse     ScanWarningKind = "file_partial_parse"
	ScanWarningFilePartialSemantics ScanWarningKind = "file_partial_semantics"
	// ScanWarningTargetUnscanned is a coverage gap: the engine failed on a
	// target, so its absence from the findings proves nothing.
	ScanWarningTargetUnscanned ScanWarningKind = "target_unscanned"
	// ScanWarningSourceMoved marks files that changed during scanning, leaving coverage uncertain.
	ScanWarningSourceMoved ScanWarningKind = "source_moved"
)

type CodeScan struct {
	DetailPrunedAt string             `json:"detail_pruned_at,omitempty"`
	ID             string             `json:"id"`
	CanonicalPath  string             `json:"canonical_path,omitempty"`
	Categories     []ScanCategory     `json:"categories"`
	ScannerID      string             `json:"scanner_id,omitempty"`
	ClaimedBy      string             `json:"-"`
	ClaimToken     string             `json:"-"`
	Attempt        int                `json:"-"`
	HeartbeatAt    *time.Time         `json:"-"`
	LeaseExpiresAt *time.Time         `json:"-"`
	Runtime        *ScanRuntimePolicy `json:"runtime,omitempty"`
	Progress       *ScanProgress      `json:"progress,omitempty"`
	// Delta is present on a path-scoped scan that compared each changed file
	// with the version its scanner last covered.
	Delta         *ScanDelta     `json:"delta,omitempty"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	LongRunningAt *time.Time     `json:"long_running_at,omitempty"`
	LongRunning   bool           `json:"long_running"`
	Status        CodeScanStatus `json:"status"`
	// Context IDs identify the selected binding projection.
	AssessmentID         string                 `json:"assessment_id,omitempty"`
	FindingSetID         string                 `json:"finding_set_id,omitempty"`
	TargetKind           ScanTargetKind         `json:"target_kind,omitempty"`
	TargetPaths          []string               `json:"target_paths,omitempty"`
	DeletedPaths         []string               `json:"deleted_paths,omitempty"`
	CoverageStatus       ScanCoverageStatus     `json:"coverage_status,omitempty"`
	FailureCode          string                 `json:"failure_code,omitempty"`
	ExecutionFingerprint string                 `json:"execution_fingerprint,omitempty"`
	FingerprintScheme    string                 `json:"fingerprint_scheme,omitempty"`
	ExecutionManifest    *ScanExecutionManifest `json:"execution_manifest,omitempty"`
	SourceCaptureQuality string                 `json:"source_capture_quality,omitempty"`
	SourceAdmissionMode  string                 `json:"source_admission_mode,omitempty"`
	FindingsCount        int                    `json:"findings_count"`
	FindingsStored       int                    `json:"findings_stored,omitempty"`
	// FindingsMerged counts rows folded into shared advisories.
	FindingsMerged        int                    `json:"findings_merged,omitempty"`
	FindingsByLevel       map[string]int         `json:"findings_by_level,omitempty"`
	FindingsByKind        map[string]int         `json:"findings_by_kind,omitempty"`
	UnmappedCount         int                    `json:"unmapped_count,omitempty"`
	TopLocations          []BoardScanTopLocation `json:"top_locations,omitempty"`
	AgentBudget           *ScanAgentBudget       `json:"agent_budget,omitempty"`
	ScanScope             string                 `json:"scan_scope,omitempty"`
	SupportedQueryFilters []string               `json:"supported_query_filters,omitempty"`
	Guidance              []ScanGuidanceSummary  `json:"guidance,omitempty"`
	Findings              []SecurityFinding      `json:"findings,omitempty"`
	Ignored               []ScanIgnoredFinding   `json:"ignored,omitempty"`
	Warnings              []ScanWarning          `json:"warnings,omitempty"`
	WarningSummary        []ScanWarningSummary   `json:"warning_summary,omitempty"`
	Result                json.RawMessage        `json:"result,omitempty"`
	CreatedAt             time.Time              `json:"created_at"`
	CompletedAt           *time.Time             `json:"completed_at,omitempty"`
	DelegationID          string                 `json:"delegation_id,omitempty"`
	WorkflowRunID         string                 `json:"workflow_run_id,omitempty"`
	SessionID             string                 `json:"session_id,omitempty"`
	HeadSHA               string                 `json:"head_sha,omitempty"`
	SourceSnapshotID      string                 `json:"source_snapshot_id,omitempty"`
	ReplacementScanID     string                 `json:"replacement_scan_id,omitempty"`
	Trigger               ScanTrigger            `json:"trigger,omitempty"`
	Error                 string                 `json:"error,omitempty"`
}
