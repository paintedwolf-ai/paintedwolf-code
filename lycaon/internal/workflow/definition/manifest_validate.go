package definition

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

func validateManifestSchema(m Manifest) error {
	if err := validateWorkflowParameters(m); err != nil {
		return err
	}
	for _, p := range m.PhaseDefs {
		if err := validatePhaseDef(p); err != nil {
			return err
		}
	}
	if err := validatePhaseTransitions(m); err != nil {
		return err
	}
	if err := validateVerifyLoopManifest(m); err != nil {
		return err
	}
	surfaces, err := surface.CompileToolPlans(1)
	if err != nil {
		return fmt.Errorf("workflow manifest %s: load coordinator surfaces: %w", m.ID, err)
	}
	knownSurfaces := make(map[string]struct{}, len(surfaces))
	for id := range surfaces {
		knownSurfaces[id] = struct{}{}
	}
	knownProfiles, err := surface.LoadSurfaceProfiles("")
	if err != nil {
		return err
	}
	return validateManifestSurfaceBinding(m, knownSurfaces, knownProfiles)
}

// ValidatePhaseTargets checks that initial and next phase references are valid.
func ValidatePhaseTargets(m Manifest) error {
	known := make(map[string]struct{}, len(m.PhaseDefs))
	for _, phase := range m.PhaseDefs {
		known[phase.ID] = struct{}{}
	}
	for _, phase := range m.PhaseDefs {
		if phase.ReviewLoop != nil && phase.ReviewLoop.ReconcilesPhase != "" {
			target, ok := m.PhaseByID(phase.ReviewLoop.ReconcilesPhase)
			if !ok || target.ID == phase.ID || target.ReviewLoop == nil {
				return fmt.Errorf("phase %q: reconciles_phase must reference a different review phase", phase.ID)
			}
			if len(phase.ReviewLoop.CoverageReviewers) > 0 && !target.ReviewLoop.CarriesCoverage() {
				return fmt.Errorf("phase %q: coverage reviewers require a reconciled coverage verdict", phase.ID)
			}
		}
		for field, target := range map[string]string{"next": phase.Next, "child_next": phase.ChildNext} {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			if phase.Terminal {
				return fmt.Errorf("workflow manifest %s: terminal phase %q cannot declare %s", m.ID, phase.ID, field)
			}
			if _, ok := known[target]; !ok {
				return fmt.Errorf("workflow manifest %s: phase %q %s references unknown phase %q", m.ID, phase.ID, field, target)
			}
			if field == "child_next" && target == phase.ID {
				return fmt.Errorf("workflow manifest %s: phase %q child_next must leave the phase", m.ID, phase.ID)
			}
		}
	}
	return nil
}

func validateWorkflowParameters(m Manifest) error {
	for name, spec := range m.Parameters {
		switch spec.Type {
		case "depth", "boolean":
		default:
			return fmt.Errorf("workflow manifest %s: parameter %q has unsupported type %q", m.ID, name, spec.Type)
		}
		if spec.Default == "" {
			continue
		}
		if _, err := normalizeWorkflowParameter(spec, spec.Default); err != nil {
			return fmt.Errorf("workflow manifest %s: parameter %q default: %w", m.ID, name, err)
		}
	}
	return nil
}

// validatePhaseTransitions checks transitions against pre-prune PhaseDefs.
func validatePhaseTransitions(m Manifest) error {
	byID := make(map[string]struct{}, len(m.PhaseDefs))
	for _, p := range m.PhaseDefs {
		if p.ID != "" {
			byID[p.ID] = struct{}{}
		}
	}
	for _, p := range m.PhaseDefs {
		for _, t := range p.Transitions {
			if _, ok := byID[t.To]; !ok {
				return fmt.Errorf("workflow manifest %s: phase %q transition %q: unknown to %q", m.ID, p.ID, t.ID, t.To)
			}
			if when := strings.TrimSpace(t.When); when != "" {
				if conditions.IsCatalogStub(when) {
					return fmt.Errorf("workflow manifest %s: phase %q transition %q: when %q is catalog-only", m.ID, p.ID, t.ID, when)
				}
			}
		}
	}
	return nil
}

// validateVerifyLoopManifest validates verify-loop structure.
func validateVerifyLoopManifest(m Manifest) error {
	verify, ok := m.PhaseByID("verify")
	if !ok || verify.Terminal {
		return validateGatesSatisfiedPhases(m)
	}
	next := strings.TrimSpace(verify.Next)
	if next != "" && !verify.Terminal {
		if next == "done" || next == verify.ID {
			return fmt.Errorf("workflow manifest %s: verify.next %q is not a valid verify-loop target", m.ID, next)
		}
	}
	return validateGatesSatisfiedPhases(m)
}

func validateGatesSatisfiedPhases(m Manifest) error {
	for _, p := range m.PhaseDefs {
		if p.CompleteWhen == CompleteWhenGatesSatisfied && len(p.Gates) == 0 {
			return fmt.Errorf("workflow manifest %s: phase %q gates_satisfied requires gates", m.ID, p.ID)
		}
	}
	return nil
}

func validatePhaseDef(p PhaseDef) error {
	if ew := strings.TrimSpace(p.EntryWhen); ew != "" {
		if conditions.IsCatalogStub(ew) {
			return fmt.Errorf("phase %q entry_when %q is catalog-only", p.ID, ew)
		}
		if !IsKnownCompleteWhen(ew) {
			return fmt.Errorf("phase %q entry_when %q is not a known condition", p.ID, ew)
		}
	}
	for _, g := range p.Gates {
		if strings.TrimSpace(g) == "" {
			return fmt.Errorf("phase %q: empty gates entry", p.ID)
		}
	}
	if p.CompleteWhen == CompleteWhenGatesSatisfied && len(p.Gates) == 0 {
		return fmt.Errorf("phase %q: gates_satisfied requires non-empty gates", p.ID)
	}
	if childWhen := strings.TrimSpace(p.ChildCompleteWhen); childWhen != "" {
		if !IsKnownCompleteWhen(childWhen) {
			return fmt.Errorf("phase %q: child_complete_when %q is not a known condition", p.ID, childWhen)
		}
		if childWhen == CompleteWhenGatesSatisfied && len(p.ChildGates) == 0 {
			return fmt.Errorf("phase %q: child_complete_when gates_satisfied requires child_gates", p.ID)
		}
	} else if len(p.ChildGates) > 0 {
		return fmt.Errorf("phase %q: child_gates requires child_complete_when", p.ID)
	}
	if p.Closeout.Gated() {
		if p.Terminal {
			return fmt.Errorf("phase %q: controls.closeout gated is invalid on a terminal phase", p.ID)
		}
		if strings.TrimSpace(p.CompleteWhen) == "" {
			return fmt.Errorf("phase %q: controls.closeout gated requires complete_when", p.ID)
		}
		if p.HumanApproval != nil {
			return fmt.Errorf("phase %q: controls.closeout gated is invalid with human_approval (the approval park handles that finish)", p.ID)
		}
		if p.ReviewLoop != nil {
			return fmt.Errorf("phase %q: controls.closeout gated is invalid with review_loop (the verdict guard handles that closeout)", p.ID)
		}
	}
	if p.ParallelTask != nil && p.ParallelTask.MaxWorkers <= 0 {
		return fmt.Errorf("phase %q: parallel_task.max_workers must be > 0", p.ID)
	}
	if p.ParallelTask != nil {
		if p.ParallelTask.MaxReadWorkers < 0 {
			return fmt.Errorf("phase %q: parallel_task.max_read_workers must be >= 0", p.ID)
		}
		if p.ParallelTask.MaxWriteWorkers < 0 {
			return fmt.Errorf("phase %q: parallel_task.max_write_workers must be >= 0", p.ID)
		}
	}
	if p.AdvanceWhenGateMet != "" {
		switch p.AdvanceWhenGateMet {
		case AdvanceWhenGateMetAuto, AdvanceWhenGateMetCoordinator:
		default:
			return fmt.Errorf("phase %q: invalid advance.when_gate_met %q", p.ID, p.AdvanceWhenGateMet)
		}
	}
	if p.LoopExit != "" {
		switch p.LoopExit {
		case LoopExitNext, LoopExitSelf:
		default:
			return fmt.Errorf("phase %q: invalid loop.exit %q", p.ID, p.LoopExit)
		}
	}
	if len(p.Intake) > 0 && p.OnEnter.RequestUserFeedback != nil && p.OnEnter.RequestUserFeedback.ResolvedResponseType().IsChoice() {
		return fmt.Errorf("phase %q: intake cannot combine with a choice request_user_feedback", p.ID)
	}
	if p.ReviewLoop != nil {
		if strings.TrimSpace(p.ReviewLoop.EvidenceKey) == "" {
			return fmt.Errorf("phase %q: review_loop.evidence_key required", p.ID)
		}
	}
	if p.Fanout.RequireThreatModel && !PhaseHasGate(p, "fanout_planned") {
		return fmt.Errorf("phase %q: fanout.require_threat_model requires a fanout_planned gate", p.ID)
	}
	if p.Fanout.MaxAttempts < 0 || p.Fanout.MaxAttempts > 3 {
		return fmt.Errorf("phase %q: fanout.max_attempts must be between 1 and 3 when set", p.ID)
	}
	if p.Fanout.MaxAttempts > 0 && !PhaseHasGate(p, "fanout_planned") {
		return fmt.Errorf("phase %q: fanout.max_attempts requires fanout_planned", p.ID)
	}
	if err := validatePhaseExplain(p); err != nil {
		return err
	}
	return validatePhaseInvoke(p)
}

// validatePhaseExplain keeps explain notes to the phases whose silence they
// cover: ones the host holds while the coordinator has nothing to do.
func validatePhaseExplain(p PhaseDef) error {
	if p.Explain == nil {
		return nil
	}
	if !p.MayHostHold() {
		return fmt.Errorf("phase %q: explain requires a phase the host holds (on_enter obligations or a topology binding)", p.ID)
	}
	summary, body := p.Explain.Summary, p.Explain.Body
	if summary == "" {
		return fmt.Errorf("phase %q: explain.summary required", p.ID)
	}
	if strings.ContainsAny(summary, "\r\n") {
		return fmt.Errorf("phase %q: explain.summary must be one line", p.ID)
	}
	if n := utf8.RuneCountInString(summary); n > ExplainSummaryMaxRunes {
		return fmt.Errorf("phase %q: explain.summary is %d characters (max %d)", p.ID, n, ExplainSummaryMaxRunes)
	}
	if body == "" {
		return fmt.Errorf("phase %q: explain.body required", p.ID)
	}
	if n := utf8.RuneCountInString(body); n > ExplainBodyMaxRunes {
		return fmt.Errorf("phase %q: explain.body is %d characters (max %d)", p.ID, n, ExplainBodyMaxRunes)
	}
	return nil
}

func validatePhaseInvoke(p PhaseDef) error {
	if p.InvokeWorkflow == nil {
		if p.InvokeTrigger != "" {
			return fmt.Errorf("phase %q: invoke_trigger requires invoke_workflow", p.ID)
		}
		return nil
	}
	trigger := p.InvokeTrigger
	if trigger == "" {
		trigger = InvokeTriggerPhaseEnter
	}
	switch trigger {
	case InvokeTriggerPhaseEnter, InvokeTriggerCoordinatorTool:
	default:
		return fmt.Errorf("phase %q: invalid invoke_trigger %q", p.ID, trigger)
	}
	if p.InvokeWorkflow != nil && strings.TrimSpace(p.EntryWhen) != "" {
		return fmt.Errorf("phase %q: entry_when cannot combine with invoke_workflow (child handles entry)", p.ID)
	}
	if p.CompleteWhen != CompleteWhenGatesSatisfied {
		return fmt.Errorf("phase %q: invoke_workflow phases must use complete_when: gates_satisfied", p.ID)
	}
	hasChildComplete := false
	for _, g := range p.Gates {
		if strings.TrimSpace(g) == "child_run_complete" {
			hasChildComplete = true
			break
		}
	}
	if !hasChildComplete {
		return fmt.Errorf("phase %q: invoke_workflow requires gates: [child_run_complete]", p.ID)
	}
	return nil
}

// ManifestHasInvokeWorkflow reports whether any phase declares a subroutine invoke.
func ManifestHasInvokeWorkflow(m Manifest) bool {
	for _, p := range m.PhaseDefs {
		if p.InvokeWorkflow != nil {
			return true
		}
	}
	return false
}

func manifestHasHumanApproval(m Manifest) bool {
	for _, p := range m.PhaseDefs {
		if p.HumanApproval != nil {
			return true
		}
	}
	return false
}

func validateWorkflowInvocations(manifests map[string]Manifest) error {
	for _, parent := range manifests {
		for _, phase := range parent.PhaseDefs {
			if err := ValidateWorkflowInvocation(parent, phase, manifests); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateWorkflowInvocation checks a phase's child contract against the effective catalog.
func ValidateWorkflowInvocation(parent Manifest, phase PhaseDef, manifests map[string]Manifest) error {
	spec := phase.InvokeWorkflow
	if spec == nil {
		return nil
	}
	key := ManifestKey(spec.WorkflowID, spec.Version)
	child, ok := manifests[key]
	if !ok {
		return fmt.Errorf("workflow manifest %s: phase %q invokes unknown workflow %s", parent.ID, phase.ID, key)
	}
	if ManifestKey(parent.ID, parent.Version) == key {
		return fmt.Errorf("workflow manifest %s: phase %q cannot invoke itself", parent.ID, phase.ID)
	}
	if err := ManifestAllowsAsChild(child); err != nil {
		return fmt.Errorf("workflow manifest %s: phase %q: %w", parent.ID, phase.ID, err)
	}
	switch spec.Blueprint {
	case ChildBlueprintNone:
		if SupportsBlueprints(child) {
			return fmt.Errorf("workflow manifest %s: phase %q: blueprint none cannot invoke Blueprint-declaring workflow %s", parent.ID, phase.ID, key)
		}
	case ChildBlueprintInherit:
		if !SupportsBlueprints(parent) || !manifestHasHumanApproval(parent) {
			return fmt.Errorf("workflow manifest %s: phase %q: blueprint inherit requires a parent Blueprint and human approval", parent.ID, phase.ID)
		}
		if SupportsBlueprints(child) {
			return fmt.Errorf("workflow manifest %s: phase %q: blueprint inherit cannot invoke Blueprint-declaring workflow %s", parent.ID, phase.ID, key)
		}
	case ChildBlueprintOwn:
		if !SupportsBlueprints(child) {
			return fmt.Errorf("workflow manifest %s: phase %q: blueprint own requires workflow %s to declare a Blueprint", parent.ID, phase.ID, key)
		}
	default:
		return fmt.Errorf("workflow manifest %s: phase %q: invoke_workflow.blueprint required", parent.ID, phase.ID)
	}
	return nil
}

func ManifestAllowsAsChild(m Manifest) error {
	if ManifestHasInvokeWorkflow(m) {
		return fmt.Errorf("workflow %s@%s: child manifest cannot declare invoke_workflow (depth 1)",
			m.ID, m.Version)
	}
	seen := map[string]struct{}{}
	phaseID := m.FirstPhase()
	for len(seen) <= len(m.PhaseDefs) {
		def, ok := m.PhaseByID(phaseID)
		if !ok {
			return fmt.Errorf("workflow %s@%s: child path references unknown phase %q", m.ID, m.Version, phaseID)
		}
		if def.Terminal {
			return nil
		}
		if _, duplicate := seen[phaseID]; duplicate {
			return fmt.Errorf("workflow %s@%s: child workflow has no terminal child path", m.ID, m.Version)
		}
		seen[phaseID] = struct{}{}
		next, advances := m.ResolveAdvanceTargetForRun(&api.WorkflowRun{ParentRunID: new("parent")}, phaseID)
		if !advances {
			return nil
		}
		phaseID = next
	}
	return fmt.Errorf("workflow %s@%s: child workflow has no terminal child path", m.ID, m.Version)
}
