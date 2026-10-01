package definition

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HumanApprovalConfig defines one phase approval gate.
type HumanApprovalConfig struct {
	Blueprint string
	Readiness string
}

// HashBlueprintContent returns a stable SHA-256 hex digest of blueprint bytes.
func HashBlueprintContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// HumanApprovalContentMatches reports whether stored hash equals current blueprint content.
func HumanApprovalContentMatches(storedHash, content string) bool {
	storedHash = strings.TrimSpace(storedHash)
	if storedHash == "" || strings.TrimSpace(content) == "" {
		return false
	}
	return storedHash == HashBlueprintContent(content)
}

// ObligationDef is one phase-declared host obligation from on_enter.obligations.
type ObligationDef struct {
	Kind   string
	Params map[string]any
}

// HasOnEnterObligations reports whether the phase declares any host obligation.
func (p PhaseDef) HasOnEnterObligations() bool {
	return len(p.OnEnter.Obligations) > 0
}

// TopologyBound reports whether the phase settles host-dispatched worker legs.
func (p PhaseDef) TopologyBound() bool {
	return len(p.BoundStages()) > 0
}

// BoundStages lists the topology stages the phase binds.
func (p PhaseDef) BoundStages() []string {
	var out []string
	if stage := strings.TrimSpace(p.BindTopologyStage); stage != "" {
		out = append(out, stage)
	}
	for _, stage := range p.BindParallelGroup {
		if stage = strings.TrimSpace(stage); stage != "" {
			out = append(out, stage)
		}
	}
	return out
}

// TopologyStagePhases maps each topology stage the manifest binds to its phase.
func (m Manifest) TopologyStagePhases() map[string]string {
	out := map[string]string{}
	for _, p := range m.PhaseDefs {
		for _, stage := range p.BoundStages() {
			out[stage] = p.ID
		}
	}
	return out
}

// MayHostHold reports whether the host can hold this phase: it owns the
// phase's pending work until obligations or topology legs settle.
func (p PhaseDef) MayHostHold() bool {
	return p.HasOnEnterObligations() || p.TopologyBound()
}

// Explain text bounds. The summary is a chicklet title; the body a short paragraph.
const (
	ExplainSummaryMaxRunes = 72
	ExplainBodyMaxRunes    = 480
)

// SecretInputSpec is value-free metadata for a protected ask_user response.
type SecretInputSpec struct {
	Name               string `json:"name"`
	Purpose            string `json:"purpose,omitempty"`
	Scope              string `json:"scope,omitempty"`
	AgentUseTTLSeconds int64  `json:"agent_use_ttl_seconds,omitempty"`
}
