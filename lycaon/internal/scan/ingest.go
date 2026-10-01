package scan

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/scan/hints"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScanSource identifies how a scan result was produced.
type ScanSource string

const ScanSourceRegistry ScanSource = "registry"

// IngestMeta carries context for evidence ingestion.
type IngestMeta struct {
	ScanID     string
	ProjectDir string
	// EvidenceRoot is the host data dir for JSONL (~/.config/paintedwolf/projects/...).
	EvidenceRoot         string
	OverlayRootPaths     []string
	HeadSHA              string
	SourceSnapshotID     string
	DelegationID         string
	TaskID               string
	Scanner              scancatalog.ScannerContract
	Categories           []api.ScanCategory
	TouchedPaths         []string
	AssessmentID         string
	CoverageStatus       api.ScanCoverageStatus
	ExecutionManifest    *api.ScanExecutionManifest
	ExecutionFingerprint string
}

// ScanResultIngester persists normalized findings and projects budgeted guidance.
type ScanResultIngester interface {
	Ingest(ctx context.Context, source ScanSource, result *scanoutput.Result, meta IngestMeta) (evidence.Record, error)
}

// NoopIngester exercises scan execution without persisting findings.
type NoopIngester struct{}

// Ingest implements ScanResultIngester.
func (NoopIngester) Ingest(_ context.Context, _ ScanSource, _ *scanoutput.Result, _ IngestMeta) (evidence.Record, error) {
	return evidence.Record{}, nil
}

// BuildEngineProof records scanner completion evidence.
func BuildEngineProof(meta IngestMeta, source ScanSource, module scancfg.ModuleConfig) EngineProof {
	contract := meta.Scanner
	proof := EngineProof{Deterministic: DeterministicEngineProof{
		ScannerID: contract.ScannerID, Engine: contract.Engine, Driver: contract.Driver,
		ScopeKind: string(contract.Scope), DefinitionFingerprint: contract.DefinitionFingerprint,
		SourceSnapshotID: meta.SourceSnapshotID, Completed: true, Source: string(source),
		AssessmentID: meta.AssessmentID, ExecutionFingerprint: meta.ExecutionFingerprint,
		FingerprintScheme: api.ScanFingerprintScheme, CoverageStatus: meta.CoverageStatus,
		Manifest: meta.ExecutionManifest,
	}}
	if contract.Scope != scancatalog.ScopeSourceHostFloor && contract.Scope != scancatalog.ScopeSourceDriver {
		return proof
	}
	proof.SAST = &SASTEngineProof{Engine: contract.Engine, Completed: true, AnalysisMode: contract.AnalysisMode}
	if contract.Scope == scancatalog.ScopeSourceHostFloor {
		proof.SAST.RulesPath = strings.TrimSpace(module.SASTRulesPath)
	}
	return proof
}

// IngesterImpl runs merge → hints → ignore → budget → evidence for completed scans.
type IngesterImpl struct {
	SecretIgnores SecretIgnoreSource
	Inspector     inspector.Inspector
	Module        scancfg.ModuleConfig
	Budget        *scancfg.FindingBudget
	BlockOn       []string
	// OverlayRootsApply selects roots whose project scan settings apply. Nil is closed.
	OverlayRootsApply       func(ctx context.Context, rootPaths []string) []string
	RecordWithoutDelegation bool
}

// IngestArtifacts is persisted on code_scans and echoed in evidence.
type IngestArtifacts struct {
	Findings          []api.SecurityFinding       `json:"findings"`
	Ignored           []scanignore.IgnoredFinding `json:"ignored"`
	FindingsCount     int                         `json:"findings_count,omitempty"`
	FindingsStored    int                         `json:"findings_stored,omitempty"`
	FindingsMerged    int                         `json:"findings_merged,omitempty"`
	FindingsByLevel   map[string]int              `json:"findings_by_level,omitempty"`
	AgentBudget       *api.ScanAgentBudget        `json:"agent_budget,omitempty"`
	ScanScope         string                      `json:"scan_scope,omitempty"`
	SarifStatistics   map[string]any              `json:"sarif_statistics,omitempty"`
	Warnings          []api.ScanWarning           `json:"warnings,omitempty"`
	CoverageStatus    api.ScanCoverageStatus      `json:"coverage_status,omitempty"`
	GuidanceTruncated bool                        `json:"guidance_truncated,omitempty"`
}

// Ingest implements ScanResultIngester.
func (i *IngesterImpl) Ingest(ctx context.Context, source ScanSource, result *scanoutput.Result, meta IngestMeta) (evidence.Record, error) {
	if i == nil {
		return evidence.Record{}, nil
	}
	if result == nil {
		result = &scanoutput.Result{}
	}
	if !meta.Scanner.Valid() {
		return evidence.Record{}, fmt.Errorf("ingest scan: valid scanner contract required")
	}
	findings := scanfindings.StampScannerDriver(append([]api.SecurityFinding(nil), result.Findings...), meta.Scanner.ScannerID)
	rawCount := result.FindingsCount
	if rawCount == 0 {
		rawCount = len(findings)
	}
	rawByLevel := scanfindings.CountFindingsByLevel(findings)

	rootPaths := meta.OverlayRootPaths
	if len(rootPaths) == 0 && strings.TrimSpace(meta.ProjectDir) != "" {
		rootPaths = []string{meta.ProjectDir}
	}
	if err := settingsoverlay.CheckFormats(rootPaths); err != nil {
		return evidence.Record{}, err
	}
	rootPaths = i.gatedOverlayRoots(ctx, rootPaths)

	merged := scanfindings.MergeByAdvisory(findings)
	// Stored and merged counts add up to the raw count.
	mergedAway := len(findings) - len(merged)

	hintsCfg, err := hints.LoadMergedForRoots(i.Module.UserConfigDir, rootPaths)
	if err != nil {
		return evidence.Record{}, err
	}
	resolver := NewScanHintResolver(hintsCfg)
	_, stampedFindings := resolver.ResolveWithHintCodes(merged)

	// Ignored findings stay in the stored set; only guidance, budget, and verdict exclude them.
	ignoreCatalog, err := scanignore.LoadIgnoreCatalog(rootPaths)
	if err != nil {
		return evidence.Record{}, err
	}
	active, ignored := scanignore.PartitionIgnored(ignoreCatalog, stampedFindings, time.Now().UTC())
	if i.SecretIgnores != nil {
		var secretIgnored []scanignore.IgnoredFinding
		active, secretIgnored = partitionSecretIgnores(result, active, i.SecretIgnores(ctx, meta.ProjectDir))
		ignored = append(ignored, secretIgnored...)
	}
	allGuidance := resolver.Resolve(active)
	budget := i.budget()
	budgeted := budget.Apply(active, meta.TouchedPaths)
	projectedGuidance := resolver.Resolve(budgeted.Findings)

	agentBudget := budget.Echo()
	sastFloor, err := i.sastFloor()
	if err != nil {
		return evidence.Record{}, err
	}
	scanScope := ScanScopeFor(meta.Scanner, sastFloor)
	sarifStats := scanfindings.BuildSarifStatistics(stampedFindings)

	verdict := verdictFromGuidance(allGuidance, i.BlockOn)
	evType := evidenceTypeFor(meta.Categories)
	artifacts := map[string]any{
		"findings_count":        rawCount,
		"findings_stored":       len(stampedFindings),
		"findings_merged":       mergedAway,
		"findings_by_level":     rawByLevel,
		"findings":              stampedFindings,
		"guidance":              projectedGuidance,
		"ignored":               ignored,
		"guidance_truncated":    budgeted.Truncated,
		"agent_budget":          agentBudget,
		"scan_scope":            scanScope,
		"categories":            meta.Categories,
		"scanner_id":            meta.Scanner.ScannerID,
		"source":                string(source),
		"scan_id":               meta.ScanID,
		"head_sha":              meta.HeadSHA,
		"source_snapshot_id":    meta.SourceSnapshotID,
		"assessment_id":         meta.AssessmentID,
		"coverage_status":       string(meta.CoverageStatus),
		"warnings":              result.Warnings,
		"execution_fingerprint": meta.ExecutionFingerprint,
		"fingerprint_scheme":    api.ScanFingerprintScheme,
	}
	if evType == evidence.GateTypeSecurity {
		artifacts["sarif_statistics"] = sarifStats
		artifacts["engine_proof"] = BuildEngineProof(meta, source, i.Module)
		artifacts["finding_count"] = scanfindings.CountFindingsByLevel(stampedFindings)
	}

	rec := evidence.GateRecord(
		evType, "", "", verdict,
		fmt.Sprintf("%d retained findings (%d raw); coverage %s", len(stampedFindings), rawCount, meta.CoverageStatus),
		artifacts, "", "", meta.HeadSHA, 0, time.Now().UTC(),
	)

	if i.Inspector != nil && meta.DelegationID != "" {
		taskID := meta.TaskID
		if taskID == "" {
			taskID = api.ScanTaskID(meta.ScanID)
		}
		if err := i.Inspector.RecordEvidence(ctx, meta.DelegationID, taskID, rec); err != nil {
			return rec, err
		}
	} else if i.RecordWithoutDelegation && i.Inspector != nil && meta.ScanID != "" {
		taskID := meta.TaskID
		if taskID == "" {
			taskID = api.ScanTaskID(meta.ScanID)
		}
		evidenceRoot := strings.TrimSpace(meta.EvidenceRoot)
		if evidenceRoot == "" {
			slog.WarnContext(ctx, "scan evidence skipped: empty EvidenceRoot", "scan_id", meta.ScanID)
		} else if err := i.recordDirect(ctx, evidenceRoot, meta.DelegationID, taskID, rec); err != nil {
			slog.WarnContext(ctx, "scan evidence append failed", "scan_id", meta.ScanID, "evidence_root", evidenceRoot, "err", err)
		}
	}
	return rec, nil
}

// sastFloor loads the path floor applied by the bundled source scanner.
func (i *IngesterImpl) sastFloor() (SASTFloor, error) {
	cfg, err := rules.LoadPathExcludes()
	if err != nil {
		return SASTFloor{}, fmt.Errorf("scan path excludes: %w", err)
	}
	return SASTFloor{Leads: cfg.GroupLeads(), Patterns: cfg.Patterns()}, nil
}

func (i *IngesterImpl) budget() *scancfg.FindingBudget {
	if i.Budget != nil {
		return i.Budget
	}
	return scancfg.NewFindingBudget(scancfg.AgentBudgetConfig{})
}

func (i *IngesterImpl) recordDirect(ctx context.Context, projectDir, runID, slot string, rec evidence.Record) error {
	simple, ok := i.Inspector.(*inspector.SimpleInspector)
	if !ok || simple.Store == nil {
		return fmt.Errorf("direct evidence write requires SimpleInspector")
	}
	rec.RunID = runID
	rec.Slot = slot
	return simple.Store.Append(ctx, projectDir, rec)
}

func verdictFromGuidance(guidance []api.ScanGuidanceSummary, blockOn []string) evidence.GateVerdict {
	for _, g := range guidance {
		for _, sev := range blockOn {
			if strings.EqualFold(g.Severity, sev) {
				return evidence.GateVerdictFailed
			}
		}
	}
	return evidence.GateVerdictPassed
}

func evidenceTypeFor(categories []api.ScanCategory) evidence.GateType {
	for _, cat := range categories {
		switch cat {
		case api.ScanCategorySecurity, api.ScanCategorySecret, api.ScanCategorySAST,
			api.ScanCategorySCA, api.ScanCategoryContainer:
			return evidence.GateTypeSecurity
		case api.ScanCategoryLint, api.ScanCategoryTypes, api.ScanCategoryStyle, api.ScanCategoryLicense,
			api.ScanCategoryCustom:
		}
	}
	return evidence.GateTypeVerify
}

// gatedOverlayRoots keeps roots whose project scan settings apply.
func (i *IngesterImpl) gatedOverlayRoots(ctx context.Context, rootPaths []string) []string {
	if i == nil || i.OverlayRootsApply == nil {
		return nil
	}
	return i.OverlayRootsApply(ctx, rootPaths)
}
