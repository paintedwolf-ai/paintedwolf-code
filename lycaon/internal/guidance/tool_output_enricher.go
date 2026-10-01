package guidance

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
)

type ToolOutputEnricher struct {
	hints        *HintConfig
	gateFeedback *feedback.GateFeedbackCatalog

	mu                      sync.Mutex
	lastProgressState       map[string]string
	lastWorkflowProgressKey map[string]string
}

type EnrichInput struct {
	SessionID string
	Session   *api.Session

	Tool   string
	Args   map[string]any
	Output string
	// Facts prevents duplicate enrichment.
	Facts ToolResultFacts

	PlanProgress PlanProgress
	PlanContent  string
	// BlueprintPath is relative to the session root.
	BlueprintPath string
	Workflow      feedback.WorkflowEvaluationContext

	BatchPhase string
}

type EnrichResult struct {
	Output string
	// Facts records raised codes in order.
	Facts                     ToolResultFacts
	ToolSpecificBannerApplied bool
}

func NewToolOutputEnricher(hints *HintConfig, gateFeedback *feedback.GateFeedbackCatalog) *ToolOutputEnricher {
	return &ToolOutputEnricher{
		hints:                   hints,
		gateFeedback:            gateFeedback,
		lastProgressState:       make(map[string]string),
		lastWorkflowProgressKey: make(map[string]string),
	}
}

// ForgetSession drops per-session enricher maps.
func (e *ToolOutputEnricher) ForgetSession(sessionID string) {
	if e == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	e.mu.Lock()
	delete(e.lastProgressState, sessionID)
	delete(e.lastWorkflowProgressKey, sessionID)
	e.mu.Unlock()
}

func (e *ToolOutputEnricher) Enrich(ctx context.Context, in EnrichInput) EnrichResult {
	if strings.TrimSpace(in.Output) == "" || in.Session == nil {
		return EnrichResult{Output: in.Output}
	}

	acc := newEnrichAccumulator(in.Output, in.Facts)
	var toolSpecific bool
	toolSpecific = e.appendRaisedBanners(in, acc) || toolSpecific
	toolSpecific = e.renderSecretReceiptIfPresent(acc) || toolSpecific
	toolSpecific = e.renderPeriodHintIfPresent(acc) || toolSpecific
	toolSpecific = e.appendFindOverflowNarrowIfNeeded(in, acc) || toolSpecific
	toolSpecific = e.appendTUINotDrivenIfNeeded(in, acc) || toolSpecific
	if strings.TrimSpace(in.Session.ParentSessionID) == "" {
		toolSpecific = e.appendToolSpecificBanners(in, acc) || toolSpecific
		e.appendPendingFeedbackLine(in, acc)
		toolSpecific = e.appendWorkflowGateBanner(ctx, in, acc) || toolSpecific
		toolSpecific = e.appendWorkflowPhaseExitRequired(in, acc) || toolSpecific
		e.appendWorkflowProgressLine(in, acc)
		e.appendSpecProgressBannerIfNeeded(in, acc, toolSpecific)
	}
	return EnrichResult{
		Output:                    acc.body,
		Facts:                     acc.facts,
		ToolSpecificBannerApplied: toolSpecific,
	}
}

func (e *ToolOutputEnricher) appendRaisedBanners(in EnrichInput, acc *enrichAccumulator) bool {
	if e == nil || e.hints == nil {
		return false
	}
	appended := false
	for _, code := range in.Facts.Codes {
		entry, ok := e.hints.HintCodes[code]
		if !ok || strings.TrimSpace(entry.Emit) != EmitBanner || strings.Contains(acc.body, "Code: "+code) {
			continue
		}
		feedback := in.Facts.FeedbackFor(code)
		message := e.bannerMessage(code, feedback.Details)
		if message == "" {
			continue
		}
		acc.body = AppendOutputBanner(acc.body, code, message)
		appended = true
	}
	return appended
}

func (e *ToolOutputEnricher) appendFindOverflowNarrowIfNeeded(in EnrichInput, acc *enrichAccumulator) bool {
	if strings.TrimSpace(in.Tool) != "find" {
		return false
	}
	if acc.raised("FIND_OVERFLOW_NARROW") {
		return true
	}
	body, ok := toolOutputJSONBody(acc.body)
	if !ok {
		return false
	}
	var probe struct {
		View     string `json:"view"`
		Selected int    `json:"selected"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return false
	}
	if probe.View != "digest" || probe.Selected != 0 {
		return false
	}
	return acc.raise("FIND_OVERFLOW_NARROW", e.bannerMessage("FIND_OVERFLOW_NARROW", map[string]any{"tool": in.Tool}))
}

func (e *ToolOutputEnricher) appendTUINotDrivenIfNeeded(in EnrichInput, acc *enrichAccumulator) bool {
	switch strings.TrimSpace(in.Tool) {
	case "command", "verify":
	default:
		return false
	}
	if acc.raised("TUI_NOT_DRIVEN") {
		return true
	}
	if !outputLooksLikePipedTUI(acc.body) {
		return false
	}
	return acc.raise("TUI_NOT_DRIVEN", e.bannerMessage("TUI_NOT_DRIVEN", map[string]any{"tool": in.Tool}))
}

func toolOutputJSONBody(raw string) (string, bool) {
	body := raw
	if idx := strings.Index(body, "\n>>>"); idx >= 0 {
		body = strings.TrimSpace(body[:idx])
	}
	if i := strings.Index(body, "\n{"); i >= 0 {
		body = body[i+1:]
	} else if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		return "", false
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	return body, true
}

func (e *ToolOutputEnricher) appendSpecProgressBannerIfNeeded(in EnrichInput, acc *enrichAccumulator, toolSpecificApplied bool) {
	if toolSpecificApplied {
		return
	}
	if in.Workflow.RunActive && strings.TrimSpace(in.Workflow.WorkflowID) != "" && len(in.Workflow.FailedLeaves) > 0 {
		return
	}
	if in.Session == nil || in.Session.Posture != api.SessionPostureSpec {
		return
	}
	if strings.TrimSpace(in.PlanProgress.ChecklistHash) == "" || strings.TrimSpace(in.SessionID) == "" {
		return
	}
	state, err := json.Marshal(in.PlanProgress)
	if err != nil {
		return
	}
	key := string(state)
	e.mu.Lock()
	last := e.lastProgressState[in.SessionID]
	if last == key {
		e.mu.Unlock()
		return
	}
	e.lastProgressState[in.SessionID] = key
	e.mu.Unlock()

	compact := formatProgressCompact(in.PlanProgress, fmt.Sprintf("%d", in.PlanProgress.PhaseInferred))
	acc.raise("SPEC_POSTURE_PROGRESS", e.bannerMessage("SPEC_POSTURE_PROGRESS", map[string]any{
		"phase":       in.PlanProgress.PhaseInferred,
		"phase_name":  in.PlanProgress.PhaseInferredName,
		"next_action": in.PlanProgress.NextAction,
		"progress":    compact,
	}))
}

func (e *ToolOutputEnricher) appendWorkflowGateBanner(ctx context.Context, in EnrichInput, acc *enrichAccumulator) bool {
	leaves := feedback.ActionableFailedLeaves(in.Tool, in.Workflow.FailedLeaves)
	if e == nil || len(leaves) == 0 {
		return false
	}
	msg, ok := e.gateFeedbackMessage(ctx, in, leaves)
	if !ok {
		msg = e.bannerMessage("WORKFLOW_GATE_BLOCKED", map[string]any{
			"tool":    in.Tool,
			"phase":   in.Workflow.CurrentPhase,
			"blocked": strings.Join(leaves, ","),
		})
	}
	// A blocked workflow_advance is a rejection; on other tools the gate is a banner.
	if feedback.IsWorkflowAdvance(in.Tool) {
		return acc.raiseRejection("WORKFLOW_GATE_BLOCKED", msg)
	}
	return acc.raise("WORKFLOW_GATE_BLOCKED", msg)
}

func (e *ToolOutputEnricher) appendWorkflowPhaseExitRequired(in EnrichInput, acc *enrichAccumulator) bool {
	if e == nil || !in.Workflow.CoordinatorPhaseExitRequired() {
		return false
	}
	return acc.raise("WORKFLOW_PHASE_EXIT_REQUIRED", e.bannerMessage("WORKFLOW_PHASE_EXIT_REQUIRED", map[string]any{
		"workflow_id": in.Workflow.WorkflowID,
		"phase":       in.Workflow.CurrentPhase,
	}))
}

func (e *ToolOutputEnricher) gateFeedbackMessage(ctx context.Context, in EnrichInput, leaves []string) (string, bool) {
	if e == nil || e.gateFeedback == nil || len(leaves) == 0 {
		return "", false
	}
	extras := feedback.PlanStubGateExtras(in.PlanContent)
	feedbackCtx := feedback.GateFeedbackContext(in.Workflow, extras)
	for _, leaf := range leaves {
		leaf = strings.TrimSpace(leaf)
		if leaf == "" || !e.gateFeedback.Has(leaf) {
			continue
		}
		msg, err := e.gateFeedback.RenderGateFeedback(ctx, leaf, feedbackCtx)
		if err != nil || strings.TrimSpace(msg) == "" {
			continue
		}
		return msg, true
	}
	return "", false
}

func (e *ToolOutputEnricher) appendWorkflowProgressLine(in EnrichInput, acc *enrichAccumulator) {
	wf := in.Workflow
	leaves := feedback.ActionableFailedLeaves(in.Tool, wf.FailedLeaves)
	if !wf.RunActive || strings.TrimSpace(wf.CurrentPhase) == "" || len(leaves) == 0 {
		return
	}
	filtered := wf
	filtered.FailedLeaves = leaves
	key := feedback.ProgressDedupKey(filtered)
	if key == "" || strings.TrimSpace(in.SessionID) == "" {
		return
	}
	e.mu.Lock()
	last := e.lastWorkflowProgressKey[in.SessionID]
	if last == key {
		e.mu.Unlock()
		return
	}
	e.lastWorkflowProgressKey[in.SessionID] = key
	e.mu.Unlock()

	acc.appendLine("Progress: " + feedback.FormatWorkflowProgress(wf.CurrentPhase, leaves, ""))
}

func (e *ToolOutputEnricher) appendPendingFeedbackLine(in EnrichInput, acc *enrichAccumulator) {
	if in.Session == nil || in.Args == nil {
		return
	}
	line, _ := in.Args["_pending_feedback_line"].(string)
	acc.appendLine(line)
}

func (e *ToolOutputEnricher) appendToolSpecificBanners(in EnrichInput, acc *enrichAccumulator) bool {
	switch {
	case in.Tool == "web_search" && webSearchRepeatCached(acc.body):
		return acc.raise("WEB_SEARCH_DUPLICATE_QUERY",
			e.bannerMessage("WEB_SEARCH_DUPLICATE_QUERY", map[string]any{"tool": in.Tool}))
	case in.Tool == "pack_board" && boardIsEmpty(acc.body):
		return acc.raise("BOARD_EMPTY_SKIP_TO_VERIFY",
			e.bannerMessage("BOARD_EMPTY_SKIP_TO_VERIFY", map[string]any{"tool": in.Tool}))
	case in.Tool == "ask_user" && askUserPendingSuccess(acc.body) && askUserMidBatch(in):
		return acc.raise("ASK_USER_MID_BATCH",
			e.bannerMessage("ASK_USER_MID_BATCH", map[string]any{"tool": in.Tool, "batch_phase": in.BatchPhase}))
	default:
		return false
	}
}

func askUserPendingSuccess(raw string) bool {
	type probe struct {
		Status string `json:"status"`
	}
	var p probe
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return false
	}
	return strings.TrimSpace(p.Status) == "pending"
}

func askUserMidBatch(in EnrichInput) bool {
	phase := strings.TrimSpace(in.BatchPhase)
	if phase == "" {
		return false
	}
	return phase != "pre_dispatch"
}

func boardIsEmpty(raw string) bool {
	type probe struct {
		BoardChars *int `json:"board_chars"`
	}
	var p probe
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return false
	}
	return p.BoardChars != nil && *p.BoardChars == 0
}

func webSearchRepeatCached(raw string) bool {
	type probe struct {
		RepeatSearch bool `json:"repeat_search"`
	}
	var p probe
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return false
	}
	return p.RepeatSearch
}

// AppendOutputBanner adds a >>> Tool feedback block with a registered hint code.
func AppendOutputBanner(out, code, msg string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return out
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = code
	}
	block := fmt.Sprintf("%s\n%s\n%s %s", markerToolFeedback, msg, codeLinePrefix, code)
	if strings.TrimSpace(out) == "" {
		return block
	}
	return out + "\n\n" + block
}

// renderSecretReceiptIfPresent expands a host receipt marker.
func (e *ToolOutputEnricher) renderSecretReceiptIfPresent(acc *enrichAccumulator) bool {
	if e == nil || e.hints == nil || !strings.Contains(acc.body, MarkerSecretReceipt) {
		return false
	}
	const code = "SECRET_REDACTED_RECEIPT"
	entry, ok := e.hints.HintCodes[code]
	if !ok {
		return false
	}
	lines := strings.Split(acc.body, "\n")
	rendered := false
	for i, line := range lines {
		token, count, parsed := ParseSecretReceiptMarker(line)
		if !parsed {
			continue
		}
		what, _, fix := RenderHintFields(code, entry, nil)
		lines[i] = strings.TrimSpace(what+" "+fix) +
			"\nredacted: " + strconv.Itoa(count) + " · unredact: " + strconv.Quote(token) +
			"\nCode: " + code
		rendered = true
	}
	if !rendered {
		return false
	}
	acc.body = strings.Join(lines, "\n")
	acc.facts = acc.facts.WithCode(code)
	return true
}

// renderPeriodHintIfPresent expands a host period marker.
func (e *ToolOutputEnricher) renderPeriodHintIfPresent(acc *enrichAccumulator) bool {
	if e == nil || e.hints == nil || !strings.Contains(acc.body, MarkerPeriodHint) {
		return false
	}
	const code = "WEB_SEARCH_YEAR_IN_QUERY"
	entry, ok := e.hints.HintCodes[code]
	if !ok {
		return false
	}
	lines := strings.Split(acc.body, "\n")
	rendered := false
	for i, line := range lines {
		year, parsed := ParsePeriodHintMarker(line)
		if !parsed {
			continue
		}
		what, _, fix := RenderHintFields(code, entry, map[string]any{"year": year})
		lines[i] = strings.TrimSpace(what+" "+fix) + "\nCode: " + code
		rendered = true
	}
	if !rendered {
		return false
	}
	acc.body = strings.Join(lines, "\n")
	acc.facts = acc.facts.WithCode(code)
	return true
}

func (e *ToolOutputEnricher) bannerMessage(code string, ctx map[string]any) string {
	if e == nil || e.hints == nil {
		return ""
	}
	entry, ok := e.hints.HintCodes[code]
	if !ok {
		return ""
	}
	return renderHintMessage(code, entry, ctx)
}
