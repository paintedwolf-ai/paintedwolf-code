package guidance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
)

// planPhaseNames are spec-posture milestone labels (plan gate phases 0–6).
var planPhaseNames = []string{
	"Infer",
	"Research depth (stub plan file)",
	"Research tasks",
	"Expand plan",
	"Review depth",
	"Critics",
	"Handoff -> implement",
}

// PlanPhaseNames returns the spec-posture milestone labels (plan gate phases 0–6).
func PlanPhaseNames() []string {
	return append([]string(nil), planPhaseNames...)
}

// PlanProgress is a host-computed snapshot of plan workflow state at tool-eval time.
type PlanProgress struct {
	PhaseInferred     int      `json:"phase_inferred"`
	PhaseInferredName string   `json:"phase_inferred_name"`
	ProgressChecklist string   `json:"progress_checklist"`
	ChecklistHash     string   `json:"checklist_hash"`
	NextAction        string   `json:"next_action"`
	PlanPath          string   `json:"plan_path,omitempty"`
	ResearchDepth     string   `json:"research_depth,omitempty"`
	ReviewDepth       string   `json:"review_depth,omitempty"`
	ScopeDisplay      string   `json:"scope_display,omitempty"`
	BreakingDisplay   string   `json:"breaking_display,omitempty"`
	SkippedPhases     []string `json:"skipped_phases,omitempty"`
}

// DispatchSnapshot carries session/workflow counters for plan phase inference.
type DispatchSnapshot struct {
	ResearchDispatches int
	CriticDispatches   int
}

// PlanEvalFlags captures skip markers from host scaffold vars. Review depth and
// critics are one skip: the phases share a marker.
type PlanEvalFlags struct {
	ResearchSkipped bool
	ReviewSkipped   bool
}

// PlanEvalFlagsFromVars reads phase skip flags from workflow host vars.
func PlanEvalFlagsFromVars(vars map[string]any) PlanEvalFlags {
	if vars == nil {
		return PlanEvalFlags{}
	}
	return PlanEvalFlags{
		ResearchSkipped: phaseSkippedVar(vars, "research"),
		ReviewSkipped:   phaseSkippedVar(vars, "review"),
	}
}

// DispatchFromVars extracts research/critic dispatch counts when present in vars.
func DispatchFromVars(vars map[string]any) DispatchSnapshot {
	if vars == nil {
		return DispatchSnapshot{}
	}
	return DispatchSnapshot{
		ResearchDispatches: intVar(vars, "research_dispatch_count", "research.dispatches"),
		CriticDispatches:   intVar(vars, "critic_dispatch_count", "critic.dispatches"),
	}
}

// ComputePlanProgress infers the active phase and checklist from plan markdown.
func ComputePlanProgress(markdown string, flags PlanEvalFlags, snap DispatchSnapshot) PlanProgress {
	text := strings.TrimSpace(markdown)
	phase, name, items := inferPlanPhase(text, flags, snap)
	checklist := formatChecklist(items)
	sum := sha256.Sum256([]byte(checklist))
	return PlanProgress{
		PhaseInferred:     phase,
		PhaseInferredName: name,
		ProgressChecklist: checklist,
		ChecklistHash:     hex.EncodeToString(sum[:]),
		NextAction:        nextActionForPhase(phase, text, flags),
		ResearchDepth:     declaredResearchDepth(text),
		ReviewDepth:       reviewDepthSection(text),
		ScopeDisplay:      sectionSnippet(text, "## Scope", "## Plan implementation scope"),
		BreakingDisplay:   sectionSnippet(text, "## Breaking changes", "## Breaking", "## Plan breaking changes"),
		SkippedPhases:     skippedPhaseIDs(flags),
	}
}

func inferPlanPhase(text string, flags PlanEvalFlags, snap DispatchSnapshot) (int, string, []checkItem) {
	items := defaultChecklist(flags)
	if text == "" {
		return 1, planPhaseNames[1], items
	}
	if scopeOrBreakingMissing(text) {
		return 0, planPhaseNames[0], markDone(items, 0)
	}
	markDone(items, 0)
	if !conditions.PlanStubValidText(text) {
		return 1, planPhaseNames[1], items
	}
	markDone(items, 1)
	if researchGateOpen(text, flags, snap) {
		return 2, planPhaseNames[2], items
	}
	markDone(items, 2)
	// "Expand plan" is done with the stub: plan_stub_valid requires ## Approach.
	markDone(items, 3)
	if conditions.PlanSectionMissing(text, "## Plan review depth") {
		return 4, planPhaseNames[4], items
	}
	markDone(items, 4)
	if criticsGateOpen(flags, snap) {
		return 5, planPhaseNames[5], items
	}
	markDone(items, 5)
	return 6, planPhaseNames[6], markDone(items, 6)
}

type checkItem struct {
	phase int
	label string
	done  bool
}

func defaultChecklist(flags PlanEvalFlags) []checkItem {
	items := make([]checkItem, len(planPhaseNames))
	for i, name := range planPhaseNames {
		items[i] = checkItem{phase: i, label: name, done: false}
	}
	for _, id := range skippedPhaseIDs(flags) {
		if n, err := parsePhaseID(id); err == nil && n >= 0 && n < len(items) {
			items[n].done = true
		}
	}
	return items
}

func markDone(items []checkItem, through int) []checkItem {
	for i := range items {
		if items[i].phase <= through {
			items[i].done = true
		}
	}
	return items
}

func formatChecklist(items []checkItem) string {
	var b strings.Builder
	for _, it := range items {
		mark := "[ ]"
		if it.done {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "%s Phase %d — %s\n", mark, it.phase, it.label)
	}
	return strings.TrimRight(b.String(), "\n")
}

func scopeOrBreakingMissing(text string) bool {
	return conditions.PlanSectionMissing(text, "## Scope", "## Plan implementation scope") ||
		conditions.PlanSectionMissing(text, "## Breaking changes", "## Breaking", "## Plan breaking changes")
}

func researchGateOpen(text string, flags PlanEvalFlags, snap DispatchSnapshot) bool {
	if flags.ResearchSkipped {
		return false
	}
	if !conditions.ResearchRequiredText(text) {
		return false
	}
	if snap.ResearchDispatches > 0 {
		return false
	}
	return true
}

func criticsGateOpen(flags PlanEvalFlags, snap DispatchSnapshot) bool {
	if flags.ReviewSkipped {
		return false
	}
	if snap.CriticDispatches >= 2 {
		return false
	}
	return true
}

// declaredResearchDepth returns the vocabulary member the blueprint declares, or
// "" when it declares none. Display only, read from the field the gate reads.
func declaredResearchDepth(text string) string {
	declared, ok := conditions.PlanFieldDeclared(text, conditions.ResearchDepthFieldKey)
	if !ok {
		return ""
	}
	return declared.ID
}

func reviewDepthSection(text string) string {
	return sectionSnippet(text, "## Plan review depth")
}

func sectionSnippet(text string, headings ...string) string {
	for _, h := range headings {
		if body := conditions.PlanSectionContent(text, h); body != "" {
			return body
		}
	}
	return ""
}

func skippedPhaseIDs(flags PlanEvalFlags) []string {
	var out []string
	if flags.ResearchSkipped {
		out = append(out, "2")
	}
	if flags.ReviewSkipped {
		out = append(out, "4", "5")
	}
	return out
}

func nextActionForPhase(phase int, text string, flags PlanEvalFlags) string {
	switch phase {
	case 0:
		return "Write the bound blueprint: " + strings.Join(conditions.PlanStubRequiredLabels(), ", ")
	case 1:
		return "write the linked blueprint: " + strings.Join(conditions.PlanStubRequiredLabels(), ", ")
	case 2:
		if flags.ResearchSkipped {
			return "Research phase skipped — advance per tier path"
		}
		return "Task(repo-researcher) or path-explorer for research before expand"
	case 3:
		return "stub already includes ## Approach — continue to review depth / critics"
	case 4:
		return "add ## Plan review depth to the plan file"
	case 5:
		if flags.ReviewSkipped {
			return "Critics phase skipped — advance per tier path"
		}
		return "Task(plan-reviewer) and plan-reviewer-alt for dual critic evidence"
	case 6:
		return "Complete critics and obtain plan approval before handoff_implement"
	default:
		return "Continue the spec posture plan workflow"
	}
}

func phaseSkippedVar(vars map[string]any, phase string) bool {
	return conditions.DotPathTruthy(vars, "phase_skipped."+phase)
}

func intVar(vars map[string]any, keys ...string) int {
	for _, key := range keys {
		if v, ok := vars[key]; ok {
			switch n := v.(type) {
			case int:
				return n
			case int64:
				return int(n)
			case float64:
				return int(n)
			}
		}
	}
	return 0
}

func parsePhaseID(id string) (int, error) {
	var n int
	_, err := fmt.Sscanf(strings.TrimSpace(id), "%d", &n)
	return n, err
}
