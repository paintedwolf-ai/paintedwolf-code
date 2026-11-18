package catalogload

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// Grounding guard codes, by the guard that emits them.
var (
	AmbientGroundingCodes     = []string{"AMBIENT_UNGROUNDED_COMPLETION", "AMBIENT_GROUNDING_ESCALATED"}
	SynthesisGroundingCodes   = []string{"SYNTH_URL_NOT_OBSERVED", "SYNTH_HANDLE_NOT_IN_LEGS", "SYNTH_CITATIONS_REQUIRED", "SYNTH_CITATION_UNVERIFIABLE"}
	InvestigateGroundingCodes = []string{"INVEST_URL_NOT_OBSERVED", "INVEST_HANDLE_NOT_OBSERVED", "INVEST_CITATIONS_REQUIRED", "INVEST_CITATION_UNVERIFIABLE"}
	WorkerGroundingCodes      = []string{"WORKER_EVIDENCE_HANDLE_UNKNOWN", "WORKER_URL_NOT_OBSERVED", "WORKER_SCOUT_NO_SURVEY_EVIDENCE", "SURFACE_CLAIM_UNGROUNDED"}
	workerSummaryCodes        = []string{"WORKER_SUMMARY_NO_ARTIFACT", "WORKER_SUMMARY_TOO_LONG", "WORKER_TURN_NO_PROSE"}
	toolEnvelopeHintCodes     = []string{"SCAN_LIST_EMPTY", "REPO_MAP_EMPTY", "TOOL_SURVEY_BYTE_CLAMPED"}
)

// promptCodeRE finds a literal hint code a prompt template names.
var promptCodeRE = regexp.MustCompile(`(?m)Code:\s+([A-Z][A-Z0-9_]+)`)

// codesOnChannel asserts registered codes emit on one channel and, when
// goSite is set, that production Go formats or names each.
func codesOnChannel(name, emit string, goSite bool, codes ...string) invariants.Invariant {
	return invariants.Invariant{
		Name:    name,
		Anchors: hintAnchors(codes),
		Check: func(r *invariants.Run) {
			cfg := invariants.HintConfig(r.Corpus)
			var scan *invariants.GuidanceEmission
			if goSite {
				scan = r.GuidanceEmission()
			}
			for _, code := range codes {
				entry := cfg.HintCodes[code]
				if emit != "" && entry.Emit != emit {
					r.Report("hint %s emits on %q, want %q", code, entry.Emit, emit)
				}
				if goSite && !scan.HasEmissionSite(code) {
					r.Report("hint %s has no production emission site", code)
				}
			}
		},
	}
}

// hintAnchors anchor on each code being registered.
func hintAnchors(codes []string) []invariants.Anchor {
	out := make([]invariants.Anchor, 0, len(codes))
	for _, code := range codes {
		out = append(out, hintCode(code))
	}
	return out
}

func hintCode(code string) invariants.Anchor {
	return invariants.AnchorFunc("hint "+code, func(c *invariants.Corpus) error {
		if _, ok := invariants.HintConfig(c).HintCodes[code]; !ok {
			return errNotRegistered
		}
		return nil
	})
}

var guidanceCodes = []invariants.Invariant{
	codesOnChannel("ambient-grounding-codes-emit-on-their-guard", "guard:ambient_grounding", true, AmbientGroundingCodes...),
	codesOnChannel("coordinator-batch-guard-codes-emit-on-their-guard", "guard:coordinator", true,
		guard.CoordinatorBatchAlreadyClosedCode, guard.CoordinatorBatchWrongPhaseCode),
	codesOnChannel("investigate-guard-codes-emit-on-their-guard", "guard:coordinator_investigate", true, InvestigateGroundingCodes...),
	codesOnChannel("synthesis-guard-codes-emit-on-their-guard", "guard:coordinator_synthesis", true, SynthesisGroundingCodes...),
	codesOnChannel("worker-summary-guard-codes-have-emission-sites", "", true, append(append([]string{}, workerSummaryCodes...), WorkerGroundingCodes...)...),
	codesOnChannel("worker-grounding-codes-emit-on-the-worker-summary-guard", "guard:worker_summary", false, WorkerGroundingCodes...),
	codesOnChannel("host-turn-requires-wait-code-emits-on-the-coordinator-guard", "guard:coordinator", true, guard.CoordinatorHostTurnRequiresWaitCode),
	codesOnChannel("tool-envelope-hint-codes-have-production-emission", "", true, toolEnvelopeHintCodes...),
	codesOnChannel("doom-loop-hint-codes-emit-on-their-guard", "guard:doom_loop", false, "DOOM_LOOP_REPEAT", "DOOM_LOOP_REPEAT_WARN"),
	{
		// Synthesis grounding retries in session, except the missing-citation
		// warning, which the model cannot fix by retrying.
		Name:    "synthesis-grounding-retry-policy",
		Anchors: append(hintAnchors(SynthesisGroundingCodes), hintCode(guidance.SynthCitationsRequiredCode)),
		Check: func(r *invariants.Run) {
			cfg := invariants.HintConfig(r.Corpus)
			for _, code := range SynthesisGroundingCodes {
				if want := code != guidance.SynthCitationsRequiredCode; cfg.IsInSessionRetry(code) != want {
					r.Report("synthesis code %s in-session retry = %v, want %v", code, !want, want)
				}
			}
		},
	},
	{
		// A banner hint is shown only if production code names it.
		Name:    "banner-hint-codes-have-production-reference",
		Anchors: []invariants.Anchor{invariants.Decl("internal/guidance", "EmitBanner")},
		Check: func(r *invariants.Run) {
			scan := r.GuidanceEmission()
			banners := 0
			for code, entry := range invariants.HintConfig(r.Corpus).HintCodes {
				if strings.TrimSpace(entry.Emit) != guidance.EmitBanner {
					continue
				}
				banners++
				if !scan.HasEmissionSite(code) {
					r.Report("banner hint %s has no production emission site", code)
				}
			}
			r.Expect(banners, "banner hint codes")
		},
	},
	{
		// A literal Code: line in a prompt names a registered hint.
		Name:    "prompt-hint-codes-registered",
		Anchors: []invariants.Anchor{invariants.Dir("lycaon/config/packs/painted-wolf/platform/agents/prompts")},
		Check: func(r *invariants.Run) {
			cfg := invariants.HintConfig(r.Corpus)
			for _, f := range r.Tree("lycaon/config/packs/painted-wolf/platform/agents/prompts", ".md") {
				for _, line := range strings.Split(f.Text(), "\n") {
					if strings.Contains(line, "Code: {{") {
						continue
					}
					for _, m := range promptCodeRE.FindAllStringSubmatch(line, -1) {
						if _, ok := cfg.HintCodes[m[1]]; !ok {
							r.Report("prompts/%s names unregistered Code: %s", f.Rel, m[1])
						}
					}
				}
			}
		},
	},
	{
		// A posture rule denies with a registered code or a posture-namespaced one.
		Name:    "posture-rule-deny-codes-registered-or-namespaced",
		Anchors: []invariants.Anchor{invariants.Dir("lycaon/config/packs/painted-wolf/platform/host/posture-rules")},
		Check: func(r *invariants.Run) {
			cfg := invariants.HintConfig(r.Corpus)
			for _, rel := range r.Glob("lycaon/config/packs/painted-wolf/platform/host/posture-rules/*.yaml") {
				name := rel[strings.LastIndex(rel, "/")+1:]
				rulesCfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join(name))
				r.Must(err)
				for _, rule := range rulesCfg.Rules {
					deny, _ := rule.Then["deny"].(map[string]any)
					code, _ := deny["code"].(string)
					if _, known := cfg.HintCodes[code]; code != "" && !known && !postureNamespaced(code) {
						r.Report("%s rule %s denies with unknown code %s", name, rule.ID, code)
					}
				}
			}
		},
	},
	{
		// Workflow feedback and coordinator denials carry registered prose.
		Name: "workflow-feedback-deny-codes-registered",
		Anchors: hintAnchors([]string{"WORKFLOW_GATE_BLOCKED", "WORKFLOW_ADVANCE_INACTIVE", "WORKFLOW_ADVANCE_NOT_COORDINATOR_MODE",
			"WORKFLOW_TRANSITION_INACTIVE", "WORKFLOW_TRANSITION_UNKNOWN", "WORKFLOW_TRANSITION_ACTOR_DENIED",
			"WORKFLOW_TRANSITION_NOT_ARMED", "WORKFLOW_DELEGATION_NOT_READY", "DOOM_LOOP_REPEAT", "DOOM_LOOP_REPEAT_WARN"}),
		Check: func(r *invariants.Run) {
			for code, entry := range invariants.HintConfig(r.Corpus).HintCodes {
				if (strings.HasPrefix(code, "WORKFLOW_") || strings.HasPrefix(code, "DOOM_LOOP_")) && strings.TrimSpace(entry.Message) == "" {
					r.Report("hint %s has no message", code)
				}
			}
		},
	},
}

// postureNamespaced reports a deny code outside the registry by design.
func postureNamespaced(code string) bool {
	return code == "DISALLOWED_AGENT" || strings.HasPrefix(code, "WORKFLOW_") || strings.Contains(code, "_POSTURE_")
}
