package scan

import "github.com/lycaon/lycaon/pkg/api"

// DeterministicEngineProof records which engine produced a scan and over what
// source, so a result can be matched to its inputs.
type DeterministicEngineProof struct {
	ScannerID             string                     `json:"scanner_id"`
	Engine                string                     `json:"engine"`
	Driver                string                     `json:"driver"`
	ScopeKind             string                     `json:"scope_kind"`
	DefinitionFingerprint string                     `json:"definition_fingerprint"`
	SourceSnapshotID      string                     `json:"source_snapshot_id"`
	Completed             bool                       `json:"completed"`
	Source                string                     `json:"source"`
	AssessmentID          string                     `json:"assessment_id,omitempty"`
	ExecutionFingerprint  string                     `json:"execution_fingerprint"`
	FingerprintScheme     string                     `json:"fingerprint_scheme"`
	CoverageStatus        api.ScanCoverageStatus     `json:"coverage_status"`
	Manifest              *api.ScanExecutionManifest `json:"manifest,omitempty"`
}

// SASTEngineProof records the SAST engine run inside a scan.
type SASTEngineProof struct {
	AnalysisMode string `json:"analysis_mode,omitempty"`
	Engine       string `json:"engine"`
	Completed    bool   `json:"completed"`
	RulesPath    string `json:"rules_path,omitempty"`
}

// EngineProof is the ingest proof stored with a scan.
type EngineProof struct {
	Deterministic DeterministicEngineProof `json:"deterministic"`
	SAST          *SASTEngineProof         `json:"sast,omitempty"`
}

// Comparison is the finding delta between two scans, for boards and tools.
type Comparison struct {
	OldScanID         string                `json:"old_scan_id"`
	NewScanID         string                `json:"new_scan_id"`
	NewFindings       []api.SecurityFinding `json:"new_findings,omitempty"`
	ResolvedFindings  []api.SecurityFinding `json:"resolved_findings,omitempty"`
	PersistedFindings []api.SecurityFinding `json:"persisted_findings,omitempty"`
	NewCount          int                   `json:"new_count"`
	ResolvedCount     int                   `json:"resolved_count"`
	PersistedCount    int                   `json:"persisted_count"`
	NewByLevel        map[string]int        `json:"new_by_level,omitempty"`
	ResolvedByLevel   map[string]int        `json:"resolved_by_level,omitempty"`
	Truncated         bool                  `json:"truncated,omitempty"`
}
