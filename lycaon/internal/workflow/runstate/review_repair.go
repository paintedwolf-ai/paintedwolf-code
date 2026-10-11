package runstate

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/pkg/api"
)

const ReviewRepairsKey = "review_repairs"

const ReviewBlockedReason = "review_blocked"

const ReviewRepairMaxAttempts = 8

const ReviewRepairMaxRepeated = 3

// ReviewRepairResponse deduplicates results from one model response.
type ReviewRepairResponse struct {
	ID          string   `json:"id"`
	Results     []string `json:"results"`
	Fingerprint string   `json:"fingerprint"`
}

// ReviewRepair is committed with workflow state; candidate snapshots are screened.
type ReviewRepair struct {
	Snapshot           *ReviewSnapshot        `json:"snapshot,omitempty"`
	CoverageFacts      *reviewcoverage.Facts  `json:"coverage_facts,omitempty"`
	ID                 string                 `json:"id"`
	Phase              string                 `json:"phase"`
	State              string                 `json:"state"`
	Attempts           int                    `json:"attempts"`
	Repeated           int                    `json:"repeated"`
	Fingerprint        string                 `json:"fingerprint"`
	CandidateMessageID string                 `json:"candidate_message_id"`
	Diagnostics        []api.ToolFeedback     `json:"diagnostics"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
	Responses          []ReviewRepairResponse `json:"responses"`
}

// ReviewSnapshot freezes the evidence used by an incomplete report at pause.
// Its candidate remains explicitly separate from accepted verdict records.
type ReviewSnapshot struct {
	Vars        map[string]any   `json:"vars"`
	Workers     []api.WorkerTask `json:"workers"`
	Scans       []api.CodeScan   `json:"scans"`
	Verdicts    []PhaseVerdict   `json:"verdicts"`
	Candidate   map[string]any   `json:"candidate,omitempty"`
	Unavailable []string         `json:"unavailable,omitempty"`
}

// ObserveResponse counts a model response once while retaining its latest result.
func (r *ReviewRepair) ObserveResponse(response, result, fingerprint string) bool {
	for _, attempt := range r.Responses {
		if slices.Contains(attempt.Results, result) {
			return false
		}
	}
	if len(r.Responses) == 0 || r.Responses[len(r.Responses)-1].ID != response {
		if slices.ContainsFunc(r.Responses, func(a ReviewRepairResponse) bool { return a.ID == response }) {
			return false
		}
		r.Responses = append(r.Responses, ReviewRepairResponse{ID: response})
	}
	last := &r.Responses[len(r.Responses)-1]
	last.Results = append(last.Results, result)
	last.Fingerprint = fingerprint
	r.Fingerprint = fingerprint
	r.Repeated = 0
	for i := len(r.Responses) - 1; fingerprint != "" && i >= 0 && r.Responses[i].Fingerprint == fingerprint; i-- {
		r.Repeated++
	}
	return true
}

func ReadReviewRepairs(vars map[string]any) ([]ReviewRepair, error) {
	if vars == nil || vars[ReviewRepairsKey] == nil {
		return nil, nil
	}
	raw, err := json.Marshal(vars[ReviewRepairsKey])
	if err != nil {
		return nil, err
	}
	var rows []ReviewRepair
	if err = json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode review repairs: %w", err)
	}
	return rows, nil
}

func CurrentReviewRepair(vars map[string]any, phase string) (*ReviewRepair, error) {
	rows, err := ReadReviewRepairs(vars)
	if err != nil {
		return nil, err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Phase == phase {
			return &rows[i], nil
		}
	}
	return nil, nil
}

func ResolveReviewRepair(vars map[string]any, phase string) (map[string]any, error) {
	rows, err := ReadReviewRepairs(vars)
	if err != nil {
		return nil, err
	}
	changed := false
	for i := range rows {
		if rows[i].Phase == phase && (rows[i].State == "repairing" || rows[i].State == "blocked") {
			rows[i].State = "resolved"
			rows[i].UpdatedAt = time.Now().UTC()
			changed = true
		}
	}
	if !changed {
		return vars, nil
	}
	next := maps.Clone(vars)
	next[ReviewRepairsKey] = rows
	return next, nil
}

func RepairableVerdictCodes(codes []string) bool {
	for _, code := range codes {
		switch code {
		case "TOOL_ARGS_INVALID", "WORKFLOW_REVIEW_LOOP_VERDICT_INVALID", "SUBMIT_VERDICT_INVENTORY_UNACCOUNTED", "SUBMIT_VERDICT_SCAN_GROUP_UNKNOWN", "SUBMIT_VERDICT_QUESTION_INVALID", "SUBMIT_VERDICT_CITATIONS_REQUIRED", "SUBMIT_VERDICT_CITATION_UNGROUNDED", "SUBMIT_VERDICT_REVIEWER_UNCITED":
			return true
		}
	}
	return false
}

func ReviewIssueFingerprint(feedback []api.ToolFeedback) string {
	var signatures []string
	for _, f := range feedback {
		identity := ReviewDiagnosticIdentity(f.Code, f.Details)
		if identity == "" {
			return ""
		}
		signatures = append(signatures, identity)
	}
	if len(signatures) == 0 {
		return ""
	}
	slices.Sort(signatures)
	return reviewcoverage.Identity(signatures)
}

func ReviewDiagnosticIdentity(code string, details map[string]any) string {
	// Stable typed fields exclude wording, examples, and candidate text.
	sig := map[string]any{"code": code}
	for _, key := range []string{"field", "field_path", "expected_type", "json_malformed", "json_encoded", "did_you_mean"} {
		if value, ok := details[key]; ok {
			sig[key] = value
		}
	}
	for _, key := range []string{"unknown_groups", "unaccounted_group_ids", "ungrounded_sample", "schema_issues", "conflict_keys", "missing_reviewers"} {
		if value, ok := details[key]; ok {
			sig[key] = canonicalDiagnosticSet(value)
		}
	}
	if raw, ok := details["issues"]; ok {
		var issues []reviewcoverage.Issue
		if decodeDiagnosticRows(raw, &issues) {
			stable := make([]map[string]any, 0, len(issues))
			for _, issue := range issues {
				stable = append(stable, map[string]any{"kind": issue.Kind, "field_path": issue.FieldPath, "fact_id": issue.FactID})
			}
			sig["issues"] = canonicalDiagnosticSet(stable)
		}
	}
	if raw, ok := details["repairs"]; ok {
		var repairs []VerdictRepair
		if decodeDiagnosticRows(raw, &repairs) {
			stable := make([]string, 0, len(repairs))
			for _, repair := range repairs {
				identity := ReviewDiagnosticIdentity(repair.Code, repair.Details)
				if identity == "" {
					return ""
				}
				stable = append(stable, identity)
			}
			slices.Sort(stable)
			sig["repairs"] = stable
		}
	}
	if len(sig) == 1 {
		return ""
	}
	return reviewcoverage.Identity(sig)
}

func decodeDiagnosticRows(raw, out any) bool {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(encoded, out) == nil
}

func canonicalDiagnosticSet(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var rows []json.RawMessage
	if json.Unmarshal(encoded, &rows) != nil {
		return value
	}
	stable := make([]string, len(rows))
	for i, row := range rows {
		stable[i] = reviewcoverage.Identity(row)
	}
	slices.Sort(stable)
	return stable
}
