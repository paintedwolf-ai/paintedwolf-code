package scan

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

const WorkflowObligationKind = "scan"

type WorkflowRunScanLister interface {
	ListByWorkflowRunID(ctx context.Context, workflowRunID string) ([]api.CodeScan, error)
}

// WorkflowRunLedger also reads the full passes a workflow run asked for.
type WorkflowRunLedger interface {
	WorkflowRunScanLister
	FullPassesForWorkflowRun(ctx context.Context, workflowRunID string) ([]FullPass, error)
}

type WorkflowRunLookup func(ctx context.Context, workflowRunID string) (*api.WorkflowRun, error)

// ProjectDirLookup resolves a project id to its primary root.
type ProjectDirLookup func(ctx context.Context, projectID string) (string, error)

type WorkflowObligation struct {
	Triggers *TriggerService
	Ledger   WorkflowRunLedger
	Settings *settings.SecurityScannersStore
	Full     FullScanRequester
	Runs     WorkflowRunLookup
	Projects ProjectDirLookup
	History  FindingHistoryReader
	Params   func(ctx context.Context, workflowRunID, phase, kind string) (map[string]any, error)
}

// FindingHistoryReader counts what a project's deltas introduced.
type FindingHistoryReader interface {
	IntroducedSince(ctx context.Context, canonicalPath string, since time.Time, minLevel api.FindingLevel) (int, error)
}

type obligationParams struct {
	Categories []api.ScanCategory
	Full       bool
	Gate       string
}

const (
	obligationGateComplete = "complete"
	obligationGateNoNew    = "no_new"
)

// NewWorkflowObligationSpec returns a validation-only obligation.
func NewWorkflowObligationSpec() *WorkflowObligation {
	return &WorkflowObligation{}
}

func (o *WorkflowObligation) Kind() string { return WorkflowObligationKind }

func (o *WorkflowObligation) ValidateParams(params map[string]any) error {
	parsed, err := parseObligationParams(params)
	if err != nil {
		return err
	}
	if len(parsed.Categories) == 0 {
		return fmt.Errorf("categories required")
	}
	return nil
}

// OnPhaseEnter starts declared full passes. Other phases use automatic delta results.
func (o *WorkflowObligation) OnPhaseEnter(ctx context.Context, run *api.WorkflowRun, projectDir string, params map[string]any) error {
	if o == nil {
		return fmt.Errorf("scan obligation unavailable")
	}
	parsed, err := parseObligationParams(params)
	if err != nil {
		return err
	}
	if !parsed.Full {
		return nil
	}
	if o.Full == nil {
		return fmt.Errorf("full scan requester unavailable")
	}
	if strings.TrimSpace(projectDir) == "" || (o.Settings != nil && !o.Settings.Effective().Enabled) {
		return nil
	}
	bind := FullScanContext{}
	if run != nil {
		bind.WorkflowRunID = run.ID
		bind.SessionID = strings.TrimSpace(run.SessionID)
	}
	scanners := ListSelectedScanners(ctx, o.Triggers.Registry, projectDir, parsed.Categories...)
	if len(scanners) == 0 {
		return fmt.Errorf("%w %v", scancatalog.ErrNoScannerForCategories, parsed.Categories)
	}
	ids := make([]string, 0, len(scanners))
	for _, meta := range scanners {
		ids = append(ids, meta.ID)
	}
	_, err = o.Full.RequestFull(ctx, projectDir, ids, api.ScanTriggerPhaseEnter, bind)
	return err
}

// ExplainFullPass names the run's newest full pass: the one its latest phase
// entry requested or joined.
func (o *WorkflowObligation) ExplainFullPass(ctx context.Context, run *api.WorkflowRun, params map[string]any) (*api.WorkflowExplainFullPass, error) {
	if o == nil || o.Ledger == nil || run == nil {
		return nil, nil
	}
	parsed, err := parseObligationParams(params)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(run.ProjectID)
	if !parsed.Full || projectID == "" {
		return nil, nil
	}
	passes, err := o.Ledger.FullPassesForWorkflowRun(ctx, run.ID)
	if err != nil || len(passes) == 0 {
		return nil, err
	}
	return &api.WorkflowExplainFullPass{AssessmentID: passes[0].ID, ProjectID: projectID}, nil
}

// Status reports bound scan coverage or evaluates deltas since the run began.
func (o *WorkflowObligation) Status(ctx context.Context, workflowRunID, phase string) (api.WorkflowRunObligation, error) {
	out := api.WorkflowRunObligation{Kind: WorkflowObligationKind}
	if o != nil && o.Settings != nil && !o.Settings.Effective().Enabled {
		out.Status = api.ObligationStatusOff
		return out, nil
	}
	if o == nil || o.Ledger == nil {
		return out, fmt.Errorf("scan ledger unavailable")
	}
	if o.Params == nil {
		return out, fmt.Errorf("workflow obligation parameters unavailable")
	}
	params, err := o.Params(ctx, strings.TrimSpace(workflowRunID), strings.TrimSpace(phase), WorkflowObligationKind)
	if err != nil {
		return out, err
	}
	parsed, err := parseObligationParams(params)
	if err != nil {
		return out, err
	}
	if parsed.Full {
		out, err = o.fullStatus(ctx, workflowRunID)
		if err != nil || out.Status != api.ObligationStatusComplete {
			return out, err
		}
	}
	if parsed.Gate == obligationGateNoNew {
		return o.noNewStatus(ctx, workflowRunID, out)
	}
	return out, nil
}

func (o *WorkflowObligation) fullStatus(ctx context.Context, workflowRunID string) (api.WorkflowRunObligation, error) {
	out := api.WorkflowRunObligation{Kind: WorkflowObligationKind}
	scans, err := o.Ledger.ListByWorkflowRunID(ctx, strings.TrimSpace(workflowRunID))
	if err != nil {
		return out, err
	}
	passes, err := o.Ledger.FullPassesForWorkflowRun(ctx, strings.TrimSpace(workflowRunID))
	if err != nil {
		return out, err
	}
	if len(scans) == 0 && len(passes) == 0 {
		out.Status = api.ObligationStatusEmpty
		return out, nil
	}
	// The run's latest pass says which scanners have not started; earlier
	// passes are represented by the scans they bound.
	var waitingScanners, missingScanners []string
	if len(passes) > 0 {
		for _, member := range passes[0].Members {
			switch member.Phase {
			case api.FullPassMemberWaitingForScanner, api.FullPassMemberWaitingForPass:
				waitingScanners = append(waitingScanners, member.ScannerID)
			case api.FullPassMemberNotStarted:
				missingScanners = append(missingScanners, member.ScannerID)
			case api.FullPassMemberStarted:
			}
		}
	}
	var (
		findings       int
		terminal       int
		failed         bool
		warming        bool
		errMsg         string
		enginesPending []string
		perScan        []map[string]any
		scanIDs        []string
	)
	for i := range scans {
		s := &scans[i]
		scanIDs = append(scanIDs, s.ID)
		row := map[string]any{
			"id": s.ID, "scanner_id": s.ScannerID, "status": s.Status, "findings_count": s.FindingsCount,
		}
		if msg := strings.TrimSpace(s.Error); msg != "" {
			row["error"] = msg
		}
		perScan = append(perScan, row)
		findings += s.FindingsCount
		switch s.Status {
		case api.CodeScanStatusComplete:
			terminal++
			if s.CoverageStatus != api.ScanCoverageComplete {
				failed = true
				if errMsg == "" {
					errMsg = fmt.Sprintf("scanner %s coverage is %s", s.ScannerID, s.CoverageStatus)
				}
			}
		case api.CodeScanStatusFailed, api.CodeScanStatusTimedOut,
			api.CodeScanStatusCanceled, api.CodeScanStatusSuperseded:
			terminal++
			failed = true
			if errMsg == "" {
				errMsg = strings.TrimSpace(s.Error)
			}
		default:
			engine := strings.TrimSpace(s.ScannerID)
			if engine == "" {
				engine = scanCategoryLabel(s.Categories)
			}
			enginesPending = append(enginesPending, engine)
			if s.SourceSnapshotID == api.SourceSnapshotWarming {
				warming = true
			}
		}
	}
	if len(missingScanners) > 0 {
		failed = true
		if errMsg == "" {
			errMsg = fmt.Sprintf("scanner(s) %s did not start", strings.Join(missingScanners, ", "))
		}
	}
	detail := map[string]any{
		"findings_count": findings,
		"scans_total":    len(scans),
		"scans_terminal": terminal,
		"scan_ids":       scanIDs,
		"per_scan":       perScan,
	}
	switch {
	case terminal < len(scans) || len(waitingScanners) > 0:
		out.Status = api.ObligationStatusPending
		enginesPending = append(enginesPending, waitingScanners...)
		sort.Strings(enginesPending)
		detail["engines_pending"] = enginesPending
		if len(waitingScanners) > 0 {
			detail["waiting_for_pass"] = waitingScanners
		}
		if warming {
			detail["warming"] = true
		}
	case failed:
		out.Status = api.ObligationStatusFailed
		out.Error = errMsg
	default:
		out.Status = api.ObligationStatusComplete
	}
	out.Detail = detail
	return out, nil
}

func WorkflowEvidenceDigest(ledger WorkflowRunScanLister) func(ctx context.Context, workflowRunID string) string {
	return func(ctx context.Context, workflowRunID string) string {
		if ledger == nil {
			return ""
		}
		scans, err := ledger.ListByWorkflowRunID(ctx, workflowRunID)
		if err != nil || len(scans) == 0 {
			return ""
		}
		var b strings.Builder
		b.WriteString("## Scan ledger\n")
		for i := range scans {
			s := &scans[i]
			fmt.Fprintf(&b, "- `%s` %s\n", s.ID, ledgerFacts(*s))
		}
		return strings.TrimSpace(b.String())
	}
}

// noNewStatus checks findings introduced since the workflow run began.
func (o *WorkflowObligation) noNewStatus(ctx context.Context, workflowRunID string, out api.WorkflowRunObligation) (api.WorkflowRunObligation, error) {
	if o.Runs == nil || o.Projects == nil || o.History == nil {
		out.Status = api.ObligationStatusEmpty
		return out, nil
	}
	run, err := o.Runs(ctx, strings.TrimSpace(workflowRunID))
	if err != nil {
		return out, err
	}
	if run == nil {
		out.Status = api.ObligationStatusEmpty
		return out, nil
	}
	projectDir, err := o.Projects(ctx, run.ProjectID)
	if err != nil {
		return out, err
	}
	if strings.TrimSpace(projectDir) == "" {
		out.Status = api.ObligationStatusEmpty
		return out, nil
	}
	canonical, err := CanonicalPath(projectDir)
	if err != nil {
		return out, err
	}
	introduced, err := o.History.IntroducedSince(ctx, canonical, run.CreatedAt, api.FindingLevelHigh)
	if err != nil {
		return out, err
	}
	if out.Detail == nil {
		out.Detail = map[string]any{}
	}
	out.Detail["gate"] = obligationGateNoNew
	out.Detail["introduced_since_run"] = introduced
	out.Detail["since"] = run.CreatedAt
	if introduced > 0 {
		out.Status = api.ObligationStatusFailed
		out.Error = fmt.Sprintf("%d finding(s) at or above high introduced since the run began", introduced)
		return out, nil
	}
	out.Status = api.ObligationStatusComplete
	return out, nil
}

func parseObligationParams(params map[string]any) (obligationParams, error) {
	var out obligationParams
	for key := range params {
		switch key {
		case "categories", "full", "gate":
		default:
			return out, fmt.Errorf("unknown param %q (want categories, full, gate)", key)
		}
	}
	cats, err := obligationCategories(params)
	if err != nil {
		return out, err
	}
	out.Categories = cats
	if raw, ok := params["full"]; ok {
		full, ok := raw.(bool)
		if !ok {
			return out, fmt.Errorf("full must be a boolean")
		}
		out.Full = full
	}
	out.Gate = obligationGateNoNew
	if out.Full {
		out.Gate = obligationGateComplete
	}
	if raw, ok := params["gate"]; ok {
		gate, ok := raw.(string)
		if !ok || (gate != obligationGateComplete && gate != obligationGateNoNew) {
			return out, fmt.Errorf("gate must be %q or %q", obligationGateComplete, obligationGateNoNew)
		}
		if gate == obligationGateComplete && !out.Full {
			return out, fmt.Errorf("gate %q needs full: true", obligationGateComplete)
		}
		out.Gate = gate
	}
	return out, nil
}

func obligationCategories(params map[string]any) ([]api.ScanCategory, error) {
	raw, ok := params["categories"]
	if !ok {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("categories must be a list")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("categories required")
	}
	cats := make([]api.ScanCategory, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("categories entries must be non-empty strings")
		}
		cats = append(cats, api.ScanCategory(strings.TrimSpace(s)))
	}
	return ResolveScanCategories(cats)
}

func scanCategoryLabel(cats []api.ScanCategory) string {
	if len(cats) == 0 {
		return "scan"
	}
	parts := make([]string, 0, len(cats))
	for _, c := range cats {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, "+")
}

// ledgerFacts states one scan for a ledger line: its scanner, status, the
// coverage a completed scan reached, its finding count, and any error.
func ledgerFacts(s api.CodeScan) string {
	var parts []string
	if id := strings.TrimSpace(s.ScannerID); id != "" {
		parts = append(parts, id)
	}
	parts = append(parts, string(s.Status))
	if s.Status == api.CodeScanStatusComplete && s.CoverageStatus != "" {
		parts = append(parts, "coverage "+string(s.CoverageStatus))
	}
	parts = append(parts, fmt.Sprintf("%d finding(s)", s.FindingsCount))
	if msg := strings.TrimSpace(s.Error); msg != "" {
		parts = append(parts, msg)
	}
	return strings.Join(parts, " · ")
}
