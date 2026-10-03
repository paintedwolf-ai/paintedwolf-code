package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// SubmitVerdictToolResult is returned by submit_verdict.
type SubmitVerdictToolResult struct {
	OK           bool   `json:"ok"`
	Verdict      string `json:"verdict,omitempty"`
	Terminal     bool   `json:"terminal,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	IterationCap int    `json:"iteration_cap,omitempty"`
	EvidenceKey  string `json:"evidence_key,omitempty"`
	Phase        string `json:"phase,omitempty"`
}

const (
	SubmitVerdictInventoryUnaccountedCode = "SUBMIT_VERDICT_INVENTORY_UNACCOUNTED"
	SubmitVerdictUnavailableCode          = "SUBMIT_VERDICT_UNAVAILABLE"
	SubmitVerdictReviewerMissingCode      = "SUBMIT_VERDICT_REVIEWER_MISSING"
	SubmitVerdictIterationCapCode         = "SUBMIT_VERDICT_ITERATION_CAP"
)

// RegisterSubmitVerdictTool registers submit_verdict. The host validates the
// payload against the active phase's verdict_schema and, on a terminal verdict,
// satisfies evidence_passed:<key> and auto-advances.
func RegisterSubmitVerdictTool(reg *tools.DefaultRegistry, runs *RunManager) error {
	if reg == nil || runs == nil {
		return fmt.Errorf("registry and run manager required")
	}
	return reg.Register("submit_verdict", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !isCoordinatorAgent(tctx.Agent) {
			return "", fmt.Errorf("submit_verdict requires coordinator role")
		}
		active, err := runs.Store.ActiveBySession(ctx, tctx.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			return rejectSubmitVerdict(tctx, SubmitVerdictUnavailableCode, "", map[string]any{
				"reason": "no_active_workflow_run",
			})
		}
		manifest, err := runs.manifestForRun(ctx, active)
		if err != nil {
			return "", err
		}
		def, ok := manifest.PhaseByID(active.CurrentPhase)
		if !ok || def.ReviewLoop == nil {
			return rejectSubmitVerdict(tctx, SubmitVerdictUnavailableCode, active.CurrentPhase, map[string]any{
				"reason": "phase_has_no_review_loop",
			})
		}
		rl := *def.ReviewLoop
		verdict, cited, citedURLs, err := parseSubmitVerdictArgs(args)
		if err != nil {
			return rejectSubmitVerdict(tctx, ReviewLoopVerdictInvalidCode, active.CurrentPhase, verdictInvalidDetails(rl, err))
		}
		rules, err := runs.VerdictRulesFor(ctx, active)
		if err != nil {
			return "", err
		}
		if err := ValidateReviewLoopVerdict(rl, verdict, rules); err != nil {
			return rejectSubmitVerdict(tctx, ReviewLoopVerdictInvalidCode, active.CurrentPhase, verdictInvalidDetails(rl, err))
		}
		groups, err := CheckScanGroups(ctx, runs.Inventory, active.ID, VerdictScanGroups(rl, verdict))
		if err != nil {
			return "", err
		}
		setAsides, _ := ParseVerdictSetAsides(rl, verdict)
		empty, err := EmptySetAsides(ctx, runs.Inventory, active.ID, setAsides)
		if err != nil {
			return "", err
		}
		if !groups.OK() || len(empty) > 0 {
			details := scanGroupRejectDetails(groups)
			details["empty_set_asides"] = empty
			return rejectSubmitVerdict(tctx, SubmitVerdictScanGroupUnknownCode, active.CurrentPhase, details)
		}
		ctx = withVerdictOperationID(ctx, tctx.ToolCallID)
		outcome, err := runs.RecordReviewLoopVerdict(ctx, tctx.SessionID, verdict, cited, citedURLs)
		if err != nil {
			return "", err
		}
		if issue := outcome.InventoryIssue; issue != nil {
			details := guidance.OffenderHintData(issue.Offenders)
			details["reason"] = issue.Reason
			details["offender_count"] = issue.Count
			details["offenders_omitted"] = max(0, issue.Count-len(issue.Offenders))
			return rejectSubmitVerdict(tctx, SubmitVerdictInventoryUnaccountedCode, active.CurrentPhase, details)
		}
		if len(outcome.MissingAgents) > 0 {
			return rejectSubmitVerdict(tctx, SubmitVerdictReviewerMissingCode, active.CurrentPhase, map[string]any{
				"missing_reviewers": outcome.MissingAgents,
				"expected_call":     describeVerdictCall(rl),
			})
		}
		if outcome.GroundingCode != "" {
			return rejectSubmitVerdict(tctx, outcome.GroundingCode, active.CurrentPhase, verdictGroundingRejectDetails(rl, outcome))
		}
		if outcome.IterationCapExceeded {
			return rejectSubmitVerdict(tctx, SubmitVerdictIterationCapCode, active.CurrentPhase, map[string]any{
				"attempt":        outcome.Attempt,
				"iteration_cap":  reviewLoopIterationCap(rl),
				"terminal_value": VerdictEnum(rl)[0],
				"expected_call":  describeVerdictCall(rl),
			})
		}
		stampVerdictOutcome(tctx, rl, outcome, verdict)
		res := SubmitVerdictToolResult{
			OK:           true,
			Verdict:      strings.TrimSpace(verdict[workflowdef.VerdictDecisionKey]),
			Terminal:     outcome.Terminal,
			Attempt:      outcome.Attempt,
			IterationCap: reviewLoopIterationCap(rl),
			EvidenceKey:  outcome.EvidenceKey,
			Phase:        active.CurrentPhase,
		}
		return marshalSubmitVerdictResult(res)
	})
}

// parseSubmitVerdictArgs decodes {verdict: {…}, cited_evidence: […], cited_urls: […]}.
// Verdict values are coerced to strings — non-string JSON values keep their compact
// JSON text, so a model that passes a list for a string field still submits a
// schema-checkable payload.
func parseSubmitVerdictArgs(args map[string]any) (map[string]string, []api.CitationGroundingCitedEvidence, []string, error) {
	if args == nil {
		return nil, nil, nil, fmt.Errorf("verdict object required")
	}
	if err := rejectUnknownKeys(args, "submit_verdict", "verdict", "cited_evidence", "cited_urls"); err != nil {
		return nil, nil, nil, err
	}
	raw, ok := args["verdict"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, nil, nil, fmt.Errorf("verdict must be a non-empty object of schema fields")
	}
	verdict := make(map[string]string, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			verdict[k] = t
		case nil:
			verdict[k] = ""
		default:
			enc, err := json.Marshal(t)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("verdict field %q is not encodable", k)
			}
			verdict[k] = string(enc)
		}
	}
	cited, err := parseVerdictCitedEvidence(args["cited_evidence"])
	if err != nil {
		return nil, nil, nil, err
	}
	urls, err := parseVerdictCitedURLs(args["cited_urls"])
	if err != nil {
		return nil, nil, nil, err
	}
	return verdict, cited, urls, nil
}

// parseVerdictCitedURLs decodes the strict URL citation channel.
func parseVerdictCitedURLs(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("cited_urls must be an array of non-empty http(s) URLs")
	}
	var out []string
	seen := map[string]struct{}{}
	for i, item := range items {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("cited_urls[%d] must be a non-empty string", i)
		}
		s = strings.TrimSpace(s)
		parsed, err := url.ParseRequestURI(s)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return nil, fmt.Errorf("cited_urls[%d] must be an http(s) URL", i)
		}
		if _, dup := seen[s]; dup {
			return nil, fmt.Errorf("cited_urls[%d] duplicates %q", i, s)
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}

// parseVerdictCitedEvidence decodes a citation list. Each entry cites an
// observed evidence handle or a repo path (with optional line/excerpt).
func parseVerdictCitedEvidence(raw any) ([]api.CitationGroundingCitedEvidence, error) {
	if raw == nil {
		return nil, nil
	}
	rawCited, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("cited_evidence must be an array of citation objects")
	}
	var cited []api.CitationGroundingCitedEvidence
	seen := map[string]struct{}{}
	for i, item := range rawCited {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cited_evidence[%d] must be an object with handle or path/line/excerpt", i)
		}
		if err := rejectUnknownKeys(m, fmt.Sprintf("cited_evidence[%d]", i), "handle", "path", "line", "excerpt"); err != nil {
			return nil, err
		}
		ce := api.CitationGroundingCitedEvidence{
			Handle:  strings.TrimSpace(stringArg(m["handle"])),
			Path:    strings.TrimSpace(stringArg(m["path"])),
			Excerpt: strings.TrimSpace(stringArg(m["excerpt"])),
		}
		if rawLine, present := m["line"]; present {
			n, ok := rawLine.(float64)
			if !ok || n < 1 || n != float64(int(n)) {
				return nil, fmt.Errorf("cited_evidence[%d].line must be a positive integer", i)
			}
			ce.Line = int(n)
		}
		if ce.Handle == "" && ce.Path == "" {
			return nil, fmt.Errorf("cited_evidence[%d] requires handle or path", i)
		}
		if ce.Handle != "" && ce.Path != "" {
			return nil, fmt.Errorf("cited_evidence[%d] must use exactly one of handle or path", i)
		}
		if ce.Handle != "" && (ce.Line != 0 || ce.Excerpt != "") {
			return nil, fmt.Errorf("cited_evidence[%d] line/excerpt require path, not handle", i)
		}
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", ce.Handle, ce.Path, ce.Line, ce.Excerpt)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("cited_evidence[%d] duplicates an earlier citation", i)
		}
		seen[key] = struct{}{}
		cited = append(cited, ce)
	}
	return cited, nil
}

func rejectUnknownKeys(values map[string]any, object string, allowed ...string) error {
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	var unknown []string
	for key := range values {
		if _, ok := allow[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("%s has undeclared field(s): %s", object, strings.Join(unknown, ", "))
}

// describeVerdictSchema renders the manifest verdict shape.
func describeVerdictSchema(rl workflowdef.ReviewLoopDef) string {
	enum := VerdictEnum(rl)
	parts := []string{fmt.Sprintf("verdict: one of %s (first value is terminal)", strings.Join(enum, "|"))}
	for _, field := range requiredVerdictFields(rl) {
		switch strings.TrimSpace(rl.VerdictSchema[field]) {
		case workflowdef.VerdictClaimsType:
			parts = append(parts, fmt.Sprintf(
				"%s: JSON array of {id, title (required when the claim is new), statement, status: one of %s, cited_evidence, answers?, scan_group_ids?}",
				field, strings.Join(rl.StatusWords(), "|")))
			continue
		case workflowdef.VerdictSetAsidesType:
			parts = append(parts, field+": JSON array of {reason, scan_group_ids} or {reason, scanner, paths}; [] when no group is set aside")
			continue
		}
		parts = append(parts, field+": non-empty string")
	}
	return "{" + strings.Join(parts, "; ") + "}"
}

func marshalSubmitVerdictResult(res SubmitVerdictToolResult) (string, error) {
	raw, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func rejectSubmitVerdict(tctx tools.ToolContext, code, phase string, details map[string]any) (string, error) {
	if tctx.Out != nil {
		var subject *api.FeedbackSubject
		if phase = strings.TrimSpace(phase); phase != "" {
			subject = &api.FeedbackSubject{Kind: "workflow_phase", ID: phase}
		}
		tctx.Out.Facts = tctx.Out.Facts.
			WithOutcome(api.ToolResultOutcomeRejected).
			WithFeedback(code, details, subject)
	}
	data := maps.Clone(details)
	if data == nil {
		data = map[string]any{}
	}
	data["phase"] = phase
	for key, value := range details {
		data["review_"+key] = value
	}
	return "", &tools.ToolReject{Code: code, Data: data}
}

// scanGroupRejectDetails names the cited ids that are not the run's groups,
// and for any that name a scan, the group ids that scan holds in the run.
func scanGroupRejectDetails(check ScanGroupCheck) map[string]any {
	const sample = 8
	unknown := check.Unknown
	if len(unknown) > sample {
		unknown = unknown[:sample]
	}
	scans := make([]string, 0, len(check.ScanIDs))
	for id := range check.ScanIDs {
		scans = append(scans, id)
	}
	sort.Strings(scans)
	var hints []string
	for _, id := range scans {
		groups := check.ScanIDs[id]
		if len(groups) > sample {
			groups = groups[:sample]
		}
		hint := id + " is a scan id"
		if len(groups) > 0 {
			hint += "; its groups in this run include " + strings.Join(groups, ", ")
		} else {
			hint += "; none of its groups are in this run's inventory"
		}
		hints = append(hints, hint)
	}
	return map[string]any{
		"unknown_groups": unknown,
		"scan_id_groups": hints,
	}
}

func verdictInvalidDetails(rl workflowdef.ReviewLoopDef, err error) map[string]any {
	return map[string]any{
		"reason":         err.Error(),
		"verdict_schema": describeVerdictSchema(rl),
		"expected_call":  describeVerdictCall(rl),
	}
}

func describeVerdictCall(rl workflowdef.ReviewLoopDef) string {
	return "submit_verdict(verdict=" + describeVerdictSchema(rl) +
		`, cited_evidence=[{"handle":"<observed-handle>"}], cited_urls=["<observed-url>"])`
}

// verdictGroundingRejectDetails returns bounded machine-readable citation audit facts.
func verdictGroundingRejectDetails(rl workflowdef.ReviewLoopDef, out ReviewLoopVerdictOutcome) map[string]any {
	return map[string]any{
		"ungrounded_count":  out.UngroundedCount,
		"ungrounded_sample": out.UngroundedSample,
		"uncited_reviewers": out.UncitedReviewers,
		"observed_handles":  out.ObservedHandles,
		"expected_call":     describeVerdictCall(rl),
	}
}

// stampVerdictOutcome states the recorded verdict as typed result meta for Den.
func stampVerdictOutcome(tctx tools.ToolContext, rl workflowdef.ReviewLoopDef, out ReviewLoopVerdictOutcome, verdict map[string]string) {
	if tctx.Out == nil {
		return
	}
	tctx.Out.Verdict = &api.VerdictOutcome{
		Verdict:      strings.TrimSpace(verdict[workflowdef.VerdictDecisionKey]),
		Terminal:     out.Terminal,
		Attempt:      out.Attempt,
		IterationCap: reviewLoopIterationCap(rl),
		EvidenceKey:  out.EvidenceKey,
		Phase:        out.Phase,
		Grounding:    out.Grounding,
	}
}
