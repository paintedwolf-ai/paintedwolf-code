package oar

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// HostDetectors binds the implementations available in this host. A nil matcher
// permits document compilation; attempting observation without one raises.
func HostDetectors(matcher *secretmatch.Matcher) *DetectorRegistry {
	registry := NewDetectorRegistry()
	registry.Register(SecretMatchDetector{Matcher: matcher})
	return registry
}

// hostCoreAdapters maps each core anchor to the local anchor this host invokes
// and delivers at; a capability document must map core anchors the same way.
var hostCoreAdapters = map[string]string{
	CoreAnchorToolPreInvoke:   AnchorToolPreInvoke,
	CoreAnchorToolHandler:     AnchorToolHandler,
	CoreAnchorToolPostInvoke:  AnchorToolPost,
	CoreAnchorAgentPostTurn:   AnchorCoordinatorPostTurn,
	CoreAnchorAgentFinalize:   AnchorWorkerFinalize,
	CoreAnchorModelInput:      AnchorContentInput,
	CoreAnchorModelOutput:     AnchorContentOutput,
	CoreAnchorModelToolResult: AnchorContentToolResult,
}

func (p *CapabilityDocument) requireHostAdapters() error {
	native := map[string]bool{AnchorCoordinatorPreInvoke: true, AnchorSessionPreInvoke: true, AnchorCredentialAssignment: true, AnchorToolRejected: true, AnchorWorkerReportCheck: true, AnchorCoordinatorCloseoutCheck: true}
	for core, local := range hostCoreAdapters {
		native[local] = true
		if declared, ok := p.Anchors.Core[core]; ok && declared != local {
			return fmt.Errorf("[OAR-PROF-10] anchor %s requires host adapter %s, got %s", core, local, declared)
		}
	}
	for _, name := range p.Anchors.Host {
		if !native[name] {
			return fmt.Errorf("[OAR-PROF-10] no host adapter for anchor %s", name)
		}
	}
	facts := map[string]string{}
	eachDeclaredFact(func(d factDecl) {
		if d.tier == FactTierHost {
			facts[publishedName(d.name, d.tier)] = factTypeString(d.typ)
		}
	})
	for _, d := range occurrenceFactDecls {
		facts[publishedName(d.name, d.tier)] = factTypeString(d.typ)
	}
	for _, f := range observationFns {
		if f.tier == FactTierHost {
			facts[publishedName(f.name, f.tier)] = "(string) -> " + factTypeString(f.ret)
		}
	}
	for _, declared := range p.HostFacts {
		if facts[declared.Name] != strings.TrimSpace(declared.Type) {
			return fmt.Errorf("[OAR-FACT-24] host_facts %s has no provider of type %s", declared.Name, declared.Type)
		}
	}
	registry := HostDetectors(nil)
	for _, ref := range p.Detectors {
		if registry.Lookup(ref) == nil {
			return fmt.Errorf("[OAR-OPS-12] no host detector %s", ref)
		}
	}
	return nil
}

// ValidateHostRuleSet refuses transformations at boundaries whose deliveries
// cannot represent rewritten content ([OAR-PROF-10]).
func ValidateHostRuleSet(rules *RuleSet) error {
	for _, rule := range rules.All() {
		if rule.Effect != EffectTransform {
			continue
		}
		switch rule.Anchor {
		case AnchorToolPost, AnchorContentOutput, AnchorContentToolResult, AnchorCredentialAssignment:
		default:
			return fmt.Errorf("[OAR-PROF-10] rule %s: transform cannot be delivered at anchor %s", rule.Qualified(), rule.Anchor)
		}
	}
	return nil
}
