package workflow

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/session/profiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ComposePolicy is deployment compose-time policy loaded from compose-policy.yaml.
type ComposePolicy struct {
	MaxPhases                   int
	RequireExtends              bool
	CoordinatorOnly             bool
	AllowedExtends              []string
	PostureRequiredGates        map[string][]string
	RequiredPhaseIDsWhenExtends map[string][]string
}

// ComposePolicyInput is input to ComposePolicy.Apply after vocabulary validation.
type ComposePolicyInput struct {
	Raw            workflowdef.Manifest
	Effective      workflowdef.Manifest
	ExtendsRef     string
	SessionPosture api.SessionPosture
	PersistTier    bool
}

// LoadComposePolicy reads bundled compose-policy.yaml.
func LoadComposePolicy() (*ComposePolicy, error) {
	data, err := config.Read(config.ComposePolicy)
	if err != nil {
		return nil, err
	}
	var raw struct {
		MaxPhases                   int                 `yaml:"max_phases"`
		RequireExtends              bool                `yaml:"require_extends"`
		CoordinatorOnly             bool                `yaml:"coordinator_only"`
		AllowedExtends              []string            `yaml:"allowed_extends"`
		PostureRequiredGates        map[string][]string `yaml:"posture_required_gates"`
		RequiredPhaseIDsWhenExtends map[string][]string `yaml:"required_phase_ids_when_extends"`
	}
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, fmt.Errorf("parse compose policy: %w", err)
	}
	maxPhases := raw.MaxPhases
	if maxPhases <= 0 {
		maxPhases = SessionComposeMaxPhases
	}
	return &ComposePolicy{
		MaxPhases:                   maxPhases,
		RequireExtends:              raw.RequireExtends,
		CoordinatorOnly:             raw.CoordinatorOnly,
		AllowedExtends:              append([]string(nil), raw.AllowedExtends...),
		PostureRequiredGates:        copyPostureGates(raw.PostureRequiredGates),
		RequiredPhaseIDsWhenExtends: copyRequiredPhases(raw.RequiredPhaseIDsWhenExtends),
	}, nil
}

func copyPostureGates(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func copyRequiredPhases(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// MaxPhasesCap returns the session compose phase cap from policy or default.
func (p *ComposePolicy) MaxPhasesCap() int {
	if p == nil || p.MaxPhases <= 0 {
		return SessionComposeMaxPhases
	}
	return p.MaxPhases
}

// Apply enforces compose policy after Composer.validateBody; returns structured 422 errors.
func (p *ComposePolicy) Apply(in ComposePolicyInput) []api.ComposeValidationError {
	if p == nil {
		return nil
	}
	var out []api.ComposeValidationError
	if p.RequireExtends && !in.PersistTier && strings.TrimSpace(in.ExtendsRef) == "" {
		out = append(out, api.ComposeValidationError{
			Field:   "extends",
			Code:    "extends_required",
			Message: "session compose requires extends when require_extends is enabled",
		})
	}
	if ref := strings.TrimSpace(in.ExtendsRef); ref != "" && len(p.AllowedExtends) > 0 && !in.PersistTier {
		if !allowedExtendsRef(ref, p.AllowedExtends) {
			out = append(out, api.ComposeValidationError{
				Field:   "extends",
				Code:    "extends_not_allowed",
				Message: fmt.Sprintf("extends parent %q is not in compose policy allowlist", ref),
			})
		}
	}
	if ref := strings.TrimSpace(in.ExtendsRef); ref != "" {
		if required, ok := p.RequiredPhaseIDsWhenExtends[ref]; ok {
			phaseIDs := map[string]struct{}{}
			for _, id := range in.Effective.Phases {
				phaseIDs[id] = struct{}{}
			}
			for _, id := range required {
				if _, ok := phaseIDs[id]; !ok {
					out = append(out, api.ComposeValidationError{
						Field:   "phases",
						Code:    "required_phase_missing",
						Message: fmt.Sprintf("extends %s requires phase %q in effective manifest", ref, id),
					})
				}
			}
		}
	}
	posture := resolvePolicyPosture(in.SessionPosture, in.Effective.InitialPosture)
	if gates, ok := p.PostureRequiredGates[string(posture)]; ok {
		out = append(out, validatePostureRequiredGates(posture, gates, in.Effective)...)
	}
	return out
}

func allowedExtendsRef(ref string, allowlist []string) bool {
	ref = strings.TrimSpace(ref)
	for _, allowed := range allowlist {
		if strings.TrimSpace(allowed) == ref {
			return true
		}
	}
	return false
}

func resolvePolicyPosture(sessionPosture api.SessionPosture, manifestInitial string) api.SessionPosture {
	if ip := strings.TrimSpace(manifestInitial); ip != "" && profiles.ValidSessionPosture(ip) {
		return api.SessionPosture(ip)
	}
	return sessionPosture
}

func validatePostureRequiredGates(posture api.SessionPosture, required []string, effective workflowdef.Manifest) []api.ComposeValidationError {
	if len(required) == 0 {
		return nil
	}
	allPreds := manifestPredicateIDs(effective)
	terminal := terminalPhaseDef(effective)
	terminalPreds := map[string]struct{}{}
	if terminal != nil {
		terminalPreds = phasePredicateIDs(*terminal)
	}
	var out []api.ComposeValidationError
	for _, gate := range required {
		gate = strings.TrimSpace(gate)
		if gate == "" {
			continue
		}
		switch posture {
		case api.SessionPostureBuild, api.SessionPostureOrchestrate:
			if _, ok := terminalPreds[gate]; !ok {
				out = append(out, api.ComposeValidationError{
					Field:   fmt.Sprintf("phases[%s]", terminalPhaseID(effective)),
					Code:    "required_gate_missing",
					Message: fmt.Sprintf("posture %q requires gate %q on terminal phase", posture, gate),
				})
			}
		default:
			if _, ok := allPreds[gate]; !ok {
				out = append(out, api.ComposeValidationError{
					Field:   "manifest",
					Code:    "required_gate_missing",
					Message: fmt.Sprintf("posture %q requires gate %q in closeout path", posture, gate),
				})
			}
		}
	}
	return out
}

func manifestPredicateIDs(m workflowdef.Manifest) map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range m.PhaseDefs {
		for id := range phasePredicateIDs(p) {
			out[id] = struct{}{}
		}
	}
	return out
}

func phasePredicateIDs(p workflowdef.PhaseDef) map[string]struct{} {
	out := map[string]struct{}{}
	for _, g := range p.Gates {
		if g = strings.TrimSpace(g); g != "" {
			out[g] = struct{}{}
		}
	}
	cw := strings.TrimSpace(p.CompleteWhen)
	if cw == "" || cw == workflowdef.CompleteWhenGatesSatisfied {
		return out
	}
	if strings.HasPrefix(cw, workflowdef.CompleteWhenGateSatisfied) {
		out[strings.TrimPrefix(cw, workflowdef.CompleteWhenGateSatisfied)] = struct{}{}
		return out
	}
	if !composeNeedsExprParser(cw) {
		out[cw] = struct{}{}
		return out
	}
	node, err := boolexpr.Parse(cw)
	if err != nil {
		return out
	}
	for _, id := range boolexpr.CollectIdents(node) {
		out[id] = struct{}{}
	}
	return out
}

func terminalPhaseDef(m workflowdef.Manifest) *workflowdef.PhaseDef {
	if len(m.PhaseDefs) == 0 {
		return nil
	}
	last := m.PhaseDefs[len(m.PhaseDefs)-1]
	return &last
}

func terminalPhaseID(m workflowdef.Manifest) string {
	if tp := terminalPhaseDef(m); tp != nil {
		return tp.ID
	}
	return ""
}
