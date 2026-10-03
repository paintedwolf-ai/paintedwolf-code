package workflow

import (
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// Phase exit kinds projected into the active-workflow inject.
const (
	PhaseExitKindProof         = "proof"
	PhaseExitKindReviewLoop    = "review_loop"
	PhaseExitKindHumanApproval = "human_approval"
	PhaseExitKindInvoke        = "invoke"
	PhaseExitKindTerminal      = "terminal"
)

// PhaseExitChoiceArm is one declared choice leave for inject teaching.
type PhaseExitChoiceArm struct {
	ID     string
	Label  string
	Actors []string
}

// CoordinatorMayFire reports whether the coordinator is a declared actor. No
// declared actors means any actor, matching the manifest default.
func (a PhaseExitChoiceArm) CoordinatorMayFire() bool {
	if len(a.Actors) == 0 {
		return true
	}
	for _, actor := range a.Actors {
		if actor == workflowdef.TransitionActorCoordinator {
			return true
		}
	}
	return false
}

// PhaseExitView is the leave recipe for the current phase. It carries facts
// only — the sentences an agent reads are written in the catalog, by
// guidance/active-workflow.md under "### Phase exit".
type PhaseExitView struct {
	Kind             string
	AdvanceAuthority string
	DepthParam       string
	// CoordinatorAdvances reports whether workflow_advance is the coordinator's
	// call on this phase, or the host's.
	CoordinatorAdvances bool
	// OpenGates are declared gates evaluated and not yet satisfied.
	OpenGates []string
	// DormantGates are event-scoped gates that have not activated. They are not
	// failures: the phase work is what activates them.
	DormantGates []string
	// CompleteWhen is a non-gate completion expression, when the phase declares one.
	CompleteWhen  string
	VerdictSchema map[string]string
	// ClaimStatuses are the status words the phase's claims may take.
	ClaimStatuses    []string
	ReviewLoopKey    string
	ReviewLoopCap    int
	FollowupAttempts int
	VerdictExample   string
	// ReviewAgents is the verdict-owed reviewer roster: required_agents plus the
	// spawnable if_spawnable subset. A terminal verdict needs a succeeded task()
	// envelope from each.
	ReviewAgents      []string
	HumanApproval     bool
	InvokeWorkflowID  string
	ChoiceTransitions []PhaseExitChoiceArm
}

// PhaseGateSnapshot is the live evaluation of one phase gate leaf at projection time.
type PhaseGateSnapshot struct {
	ID        string
	Satisfied bool
	Dormant   bool
}

// ProjectPhaseExit derives leave steps from phase state.
// A nil snapshot treats declared gates as open. reviewAgents is the resolved
// verdict-owed reviewer roster for a review_loop phase (required_agents plus the
// spawnable if_spawnable subset); empty means the declared roster.
func ProjectPhaseExit(manifest workflowdef.Manifest, phase workflowdef.PhaseDef, gates []PhaseGateSnapshot, reviewAgents []string) PhaseExitView {
	auth := string(workflowdef.EffectiveAdvancePolicy(manifest, phase))
	out := PhaseExitView{
		AdvanceAuthority:    auth,
		CoordinatorAdvances: auth == string(workflowdef.AdvanceWhenGateMetCoordinator),
		DepthParam:          strings.TrimSpace(phase.DepthParam),
	}

	switch {
	case phase.Terminal:
		out.Kind = PhaseExitKindTerminal
	case phase.ReviewLoop != nil:
		out.Kind = PhaseExitKindReviewLoop
		out.ReviewLoopKey = strings.TrimSpace(phase.ReviewLoop.EvidenceKey)
		out.ReviewLoopCap = phase.ReviewLoop.IterationCap
		if phase.ReviewLoop.FollowupAttempts > 0 {
			out.ReviewLoopCap = 0
		}
		out.FollowupAttempts = phase.ReviewLoop.FollowupAttempts
		out.VerdictExample = VerdictExample(*phase.ReviewLoop)
		if len(reviewAgents) == 0 {
			reviewAgents = dedupeReviewAgents(phase.ReviewLoop.RequiredAgents, phase.ReviewLoop.IfSpawnable)
		}
		out.ReviewAgents = append([]string(nil), reviewAgents...)
		out.VerdictSchema = maps.Clone(phase.ReviewLoop.VerdictSchema)
		out.ClaimStatuses = phase.ReviewLoop.StatusWords()
	case phase.HumanApproval != nil:
		out.Kind = PhaseExitKindHumanApproval
		out.HumanApproval = true
	case phase.InvokeWorkflow != nil:
		out.Kind = PhaseExitKindInvoke
		out.InvokeWorkflowID = strings.TrimSpace(phase.InvokeWorkflow.WorkflowID)
	default:
		out.Kind = PhaseExitKindProof
		out.OpenGates = openProofGates(phase.Gates, gates)
		out.DormantGates = dormantProofGates(gates)
		if cw := strings.TrimSpace(phase.CompleteWhen); cw != "" &&
			cw != workflowdef.CompleteWhenGatesSatisfied && len(phase.Gates) == 0 {
			out.CompleteWhen = cw
		}
	}
	out.ChoiceTransitions = choiceExitArms(phase.Transitions)
	return out
}

// openProofGates returns unsatisfied active gates.
func openProofGates(declared []string, live []PhaseGateSnapshot) []string {
	if live == nil {
		out := make([]string, 0, len(declared))
		for _, g := range declared {
			if g = strings.TrimSpace(g); g != "" {
				out = append(out, g)
			}
		}
		return out
	}
	var out []string
	for _, g := range live {
		id := strings.TrimSpace(g.ID)
		if id == "" || g.Dormant || g.Satisfied {
			continue
		}
		out = append(out, id)
	}
	return out
}

func dormantProofGates(live []PhaseGateSnapshot) []string {
	var out []string
	for _, g := range live {
		id := strings.TrimSpace(g.ID)
		if id != "" && g.Dormant && !g.Satisfied {
			out = append(out, id)
		}
	}
	return out
}

func choiceExitArms(edges []workflowdef.PhaseTransitionDef) []PhaseExitChoiceArm {
	if len(edges) == 0 {
		return nil
	}
	out := make([]PhaseExitChoiceArm, 0, len(edges))
	for _, edge := range edges {
		label := strings.TrimSpace(edge.Label)
		if label == "" {
			label = edge.ID
		}
		out = append(out, PhaseExitChoiceArm{
			ID:     edge.ID,
			Label:  label,
			Actors: append([]string(nil), edge.Actors...),
		})
	}
	return out
}

// InjectView snapshots phase exit facts for the prompt renderer.
func (exit PhaseExitView) InjectView() *inject.PhaseExitView {
	pe := &inject.PhaseExitView{
		Kind:                exit.Kind,
		DepthParam:          exit.DepthParam,
		CoordinatorAdvances: exit.CoordinatorAdvances,
		OpenGates:           append([]string(nil), exit.OpenGates...),
		DormantGates:        append([]string(nil), exit.DormantGates...),
		CompleteWhen:        exit.CompleteWhen,
		VerdictSchema:       maps.Clone(exit.VerdictSchema),
		ClaimStatuses:       append([]string(nil), exit.ClaimStatuses...),
		ReviewLoopKey:       exit.ReviewLoopKey,
		ReviewLoopCap:       exit.ReviewLoopCap,
		FollowupAttempts:    exit.FollowupAttempts,
		VerdictExample:      exit.VerdictExample,
		ReviewAgents:        append([]string(nil), exit.ReviewAgents...),
		HumanApproval:       exit.HumanApproval,
		InvokeWorkflowID:    exit.InvokeWorkflowID,
	}
	if len(exit.ChoiceTransitions) > 0 {
		pe.ChoiceTransitions = make([]inject.PhaseExitChoiceArm, len(exit.ChoiceTransitions))
		for i, arm := range exit.ChoiceTransitions {
			pe.ChoiceTransitions[i] = inject.PhaseExitChoiceArm{
				ID:                 arm.ID,
				Label:              arm.Label,
				Actors:             append([]string(nil), arm.Actors...),
				CoordinatorMayFire: arm.CoordinatorMayFire(),
			}
		}
	}
	return pe
}
