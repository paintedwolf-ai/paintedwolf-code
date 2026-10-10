package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/verdictcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// SubmitVerdictToolResult is returned by submit_verdict.
type SubmitVerdictToolResult struct {
	Questions        []reviewQuestionWork `json:"questions,omitempty"`
	FollowupAttempts int                  `json:"followup_attempts,omitempty"`
	OK               bool                 `json:"ok"`
	Verdict          string               `json:"verdict,omitempty"`
	Terminal         bool                 `json:"terminal,omitempty"`
	Attempt          int                  `json:"attempt,omitempty"`
	IterationCap     int                  `json:"iteration_cap,omitempty"`
	EvidenceKey      string               `json:"evidence_key,omitempty"`
	Phase            string               `json:"phase,omitempty"`
}

const (
	SubmitVerdictInventoryUnaccountedCode = "SUBMIT_VERDICT_INVENTORY_UNACCOUNTED"
	SubmitVerdictUnavailableCode          = "SUBMIT_VERDICT_UNAVAILABLE"
	SubmitVerdictReviewerMissingCode      = "SUBMIT_VERDICT_REVIEWER_MISSING"
	SubmitVerdictIterationCapCode         = "SUBMIT_VERDICT_ITERATION_CAP"
)

// RegisterSubmitVerdictTool registers submit_verdict. Review phases compose
// the call they accept from its catalog schema; the host validates the payload
// against the active phase's verdict_schema and, on a terminal verdict,
// satisfies evidence_passed:<key> and auto-advances.
func RegisterSubmitVerdictTool(reg *tools.DefaultRegistry, runs *RunManager) error {
	if reg == nil || runs == nil {
		return fmt.Errorf("registry and run manager required")
	}
	// The stock call schema composes a phase's accepted call when a turn
	// offered none; it is read once the tool's catalog metadata is registered.
	var stock map[string]any
	if err := reg.Register("submit_verdict", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
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
		outline := verdictOutline(tctx.TurnOfferedToolSchemas["submit_verdict"], stock, rl, manifest.ReportBrief())
		verdict, cited, citedURLs, err := parseSubmitVerdictArgs(rl, args)
		if err != nil {
			return rejectSubmitVerdict(tctx, ReviewLoopVerdictInvalidCode, active.CurrentPhase, verdictInvalidDetails(outline, err))
		}
		rules, err := runs.VerdictRulesFor(ctx, active)
		if err != nil {
			return "", err
		}
		if err := ValidateReviewLoopVerdict(rl, verdict, rules); err != nil {
			return rejectSubmitVerdict(tctx, ReviewLoopVerdictInvalidCode, active.CurrentPhase, verdictInvalidDetails(outline, err))
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
			if rejection := tools.AsToolReject(err); rejection != nil {
				return rejectSubmitVerdict(tctx, rejection.Code, active.CurrentPhase, rejection.Data)
			}
			return "", err
		}
		if repairs := verdictRepairs(outline, outcome); len(repairs) > 0 {
			primary := repairs[0]
			details := maps.Clone(primary.Details)
			details["repairs"] = repairs
			return rejectSubmitVerdict(tctx, primary.Code, active.CurrentPhase, details)
		}
		if outcome.IterationCapExceeded {
			return rejectSubmitVerdict(tctx, SubmitVerdictIterationCapCode, active.CurrentPhase, map[string]any{
				"attempt":        outcome.Attempt,
				"iteration_cap":  reviewLoopIterationCap(rl),
				"terminal_value": rl.Decisions()[0],
				"expected_call":  describeVerdictCall(outline),
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
		if rl.FollowupAttempts > 0 {
			vars, err := runs.Store.GetScaffoldVars(ctx, active.ID)
			if err != nil {
				return "", err
			}
			res.Questions, err = reviewQuestions(vars, active.CurrentPhase)
			if err != nil {
				return "", err
			}
			res.FollowupAttempts = rl.FollowupAttempts
			res.IterationCap = 0
		}
		return marshalSubmitVerdictResult(res)
	}); err != nil {
		return err
	}
	meta, ok := reg.Meta("submit_verdict")
	if !ok {
		return fmt.Errorf("submit_verdict registered without catalog metadata")
	}
	if err := verdictcall.CheckFragments(meta.ArgsSchema); err != nil {
		return err
	}
	stock = meta.ArgsSchema
	return nil
}

// Structured verdict fields retain JSON in the durable string-valued record.
func parseSubmitVerdictArgs(def workflowdef.ReviewLoopDef, args map[string]any) (map[string]string, []api.CitationGroundingCitedEvidence, []string, error) {
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
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		kind, declared := def.VerdictSchema[key]
		encoded, err := encodeVerdictField(kind, declared, key, raw[key])
		if err != nil {
			return nil, nil, nil, err
		}
		verdict[key] = encoded
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

// encodeVerdictField checks a declared member against its declared type. An
// undeclared member keeps its value so validation can name it as undeclared
// instead of faulting it for a type the schema never asked for.
func encodeVerdictField(kind string, declared bool, field string, value any) (string, error) {
	if !declared {
		if text, ok := value.(string); ok {
			return text, nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode verdict field %q: %w", field, err)
		}
		return string(raw), nil
	}
	switch kind {
	case workflowdef.VerdictClaimsType, workflowdef.VerdictSetAsidesType:
		if _, ok := value.([]any); !ok {
			return "", fmt.Errorf("verdict field %q must be an array", field)
		}
	case workflowdef.VerdictCoverageType:
		if _, ok := value.(map[string]any); !ok {
			return "", fmt.Errorf("verdict field %q must be an object", field)
		}
	default:
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("verdict field %q must be a string", field)
		}
		return text, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode verdict field %q: %w", field, err)
	}
	return string(raw), nil
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
// observed evidence handle, a repo path with optional line/excerpt, or both.
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
		if ce.Path == "" && (ce.Line != 0 || ce.Excerpt != "") {
			return nil, fmt.Errorf("cited_evidence[%d] line/excerpt require path", i)
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

func verdictInvalidDetails(outline string, err error) map[string]any {
	return map[string]any{
		"reason":        err.Error(),
		"expected_call": describeVerdictCall(outline),
	}
}

// describeVerdictCall renders the call a review phase accepts from its
// verdict outline; the offered submit_verdict schema carries the types.
func describeVerdictCall(outline string) string {
	return "submit_verdict(verdict=" + outline +
		`, cited_evidence=[{"handle":"<observed-handle>"}], cited_urls=["<observed-url>"])`
}

// verdictOutline renders the call a review phase accepts: the phase schema
// this turn offered, or the stock catalog's composition for the phase.
func verdictOutline(offered, stock map[string]any, rl workflowdef.ReviewLoopDef, brief *workflowdef.Brief) string {
	if offeredPhaseCall(offered) {
		return verdictcall.Outline(offered)
	}
	call, err := verdictcall.Compose(stock, rl, brief)
	if err != nil {
		return "{verdict: " + strings.Join(rl.Decisions(), "|") + "}"
	}
	return verdictcall.Outline(call)
}

// offeredPhaseCall reports whether an offered submit_verdict schema is a
// phase composition, whose decision member declares its values.
func offeredPhaseCall(offered map[string]any) bool {
	props, _ := offered["properties"].(map[string]any)
	verdict, _ := props["verdict"].(map[string]any)
	members, _ := verdict["properties"].(map[string]any)
	decision, _ := members[workflowdef.VerdictDecisionKey].(map[string]any)
	_, ok := decision["enum"]
	return ok
}

// verdictGroundingRejectDetails returns bounded machine-readable citation audit facts.
func verdictGroundingRejectDetails(outline string, out ReviewLoopVerdictOutcome) map[string]any {
	return map[string]any{
		"ungrounded_count":  out.UngroundedCount,
		"ungrounded_sample": out.UngroundedSample,
		"uncited_reviewers": out.UncitedReviewers,
		"observed_handles":  out.ObservedHandles,
		"expected_call":     describeVerdictCall(outline),
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

// verdictRepair retains each refusal's code and facts in the same response.
type verdictRepair struct {
	Code    string         `json:"code"`
	Details map[string]any `json:"details"`
}

func verdictRepairs(outline string, out ReviewLoopVerdictOutcome) []verdictRepair {
	var repairs []verdictRepair
	if issue := out.InventoryIssue; issue != nil {
		details := issue.Details()
		code := SubmitVerdictInventoryUnaccountedCode
		if issue.Code == SubmitVerdictScansPendingCode {
			code = SubmitVerdictScansPendingCode
		}
		repairs = append(repairs, verdictRepair{code, details})
	}
	if len(out.MissingAgents) > 0 {
		repairs = append(repairs, verdictRepair{SubmitVerdictReviewerMissingCode, map[string]any{"missing_reviewers": out.MissingAgents, "expected_call": describeVerdictCall(outline)}})
	}
	if out.GroundingCode != "" {
		repairs = append(repairs, verdictRepair{out.GroundingCode, verdictGroundingRejectDetails(outline, out)})
	}
	if out.QuestionIssue != nil {
		repairs = append(repairs, verdictRepair{out.QuestionIssue.Code, out.QuestionIssue.Data})
	}
	if issue := out.CoverageIssue; issue != nil && !slices.ContainsFunc(repairs, func(r verdictRepair) bool { return r.Code == issue.Code }) {
		details := maps.Clone(issue.Data)
		if details == nil {
			details = map[string]any{}
		}
		if _, ok := details["expected_call"]; !ok && issue.Code == ReviewLoopVerdictInvalidCode {
			details["expected_call"] = describeVerdictCall(outline)
		}
		repairs = append(repairs, verdictRepair{issue.Code, details})
	}
	return repairs
}
