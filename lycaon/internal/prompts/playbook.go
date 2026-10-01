package prompts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// PlaybookMatcher loads playbooks and merges checklists for topology/phase.
type PlaybookMatcher struct {
	byID     map[string]Playbook
	fallback *Playbook
	contract *PersonaContract
}

// playbookSource is one playbook document by origin name and captured bytes.
type playbookSource struct {
	Name string
	Data []byte
}

// LoadPlaybookMatcher reads playbooks from a host directory — a project overlay
// or a test fixture. A bare directory is not pack content, so no unit gate applies.
func LoadPlaybookMatcher(playbooksDir string, contract *PersonaContract) (*PlaybookMatcher, error) {
	dir := extpacks.OnDisk(playbooksDir)
	if dir.Empty() {
		return nil, fmt.Errorf("playbooks directory required")
	}
	entries, err := dir.List()
	if err != nil {
		return nil, err
	}
	var sources []playbookSource
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		raw, err := dir.Join(ent.Name()).Read()
		if err != nil {
			return nil, err
		}
		sources = append(sources, playbookSource{Name: ent.Name(), Data: raw})
	}
	return loadPlaybookMatcherSources(sources, contract)
}

// LoadPlaybookMatcherEffective loads effective playbooks.
func LoadPlaybookMatcherEffective(contract *PersonaContract) (*PlaybookMatcher, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	return LoadPlaybookMatcherEffectiveWithCatalog(catalog, contract)
}

// LoadPlaybookMatcherEffectiveWithCatalog compiles captured playbooks.
func LoadPlaybookMatcherEffectiveWithCatalog(catalog *extpacks.EffectiveCatalog, contract *PersonaContract) (*PlaybookMatcher, error) {
	if catalog == nil {
		return nil, fmt.Errorf("playbooks: effective catalog required")
	}
	var sources []playbookSource
	for _, id := range catalog.LoadedUnitIDs() {
		if !strings.HasPrefix(id, "playbooks/") {
			continue
		}
		at, _ := catalog.UnitPath(id)
		// Playbooks are YAML units.
		if !strings.HasSuffix(at.String(), ".yaml") {
			continue
		}
		content, _, ok := catalog.UnitContent(id)
		if !ok {
			continue
		}
		sources = append(sources, playbookSource{Name: id, Data: content})
	}
	return loadPlaybookMatcherSources(sources, contract)
}

func loadPlaybookMatcherSources(sources []playbookSource, contract *PersonaContract) (*PlaybookMatcher, error) {
	byID := make(map[string]Playbook)
	var fallback *Playbook
	for _, src := range sources {
		var pb Playbook
		if err := config.DecodeYAML(src.Data, &pb); err != nil {
			return nil, fmt.Errorf("playbook %s: %w", src.Name, err)
		}
		id := strings.TrimSpace(pb.ID)
		if id == "" {
			return nil, fmt.Errorf("playbook %s: missing id", src.Name)
		}
		if _, dup := byID[id]; dup {
			return nil, fmt.Errorf("duplicate playbook id %q", id)
		}
		byID[id] = pb
		if pb.Fallback {
			if fallback != nil {
				return nil, fmt.Errorf("multiple fallback playbooks (%q and %q)", fallback.ID, id)
			}
			cp := pb
			fallback = &cp
		}
	}
	if fallback == nil {
		return nil, fmt.Errorf("playbooks: missing fallback playbook (fallback: true)")
	}
	if contract != nil {
		if err := validateContractPlaybookIDs(contract, byID); err != nil {
			return nil, err
		}
	}
	return &PlaybookMatcher{byID: byID, fallback: fallback, contract: contract}, nil
}

func validateContractPlaybookIDs(contract *PersonaContract, byID map[string]Playbook) error {
	seen := make(map[string]struct{})
	for _, def := range contract.Agents {
		for _, id := range def.PlaybookIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := byID[id]; !ok {
				return fmt.Errorf("persona contract references unknown playbook %q", id)
			}
			seen[id] = struct{}{}
		}
	}
	for id, pb := range byID {
		if ext := strings.TrimSpace(pb.Extends); ext != "" {
			if _, ok := byID[ext]; !ok {
				return fmt.Errorf("playbook %q extends unknown playbook %q", id, ext)
			}
		}
	}
	return nil
}

// PlaybookIDs returns sorted loaded playbook ids (for parity / diagnostics).
func (m *PlaybookMatcher) PlaybookIDs() []string {
	if m == nil || len(m.byID) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m.byID))
	for id := range m.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// MatchForAgent merges checklists for agent playbook_ids when triggers match.
func (m *PlaybookMatcher) MatchForAgent(agentID, topologyPattern, phaseID string) ([]string, error) {
	if m == nil {
		return nil, fmt.Errorf("playbook matcher not configured")
	}
	ids := m.agentPlaybookIDs(agentID)
	return m.mergePlaybooks(ids, topologyPattern, phaseID)
}

// MatchPlaybook returns merged checklist for explicit playbook ids (tests).
func (m *PlaybookMatcher) MatchPlaybook(playbookIDs []string, topologyPattern, phaseID string) ([]string, error) {
	if m == nil {
		return nil, fmt.Errorf("playbook matcher not configured")
	}
	return m.mergePlaybooks(playbookIDs, topologyPattern, phaseID)
}

func (m *PlaybookMatcher) agentPlaybookIDs(agentID string) []string {
	if m.contract == nil {
		return []string{"worker-leg-default"}
	}
	def, ok := m.contract.Agents[strings.TrimSpace(agentID)]
	if !ok || len(def.PlaybookIDs) == 0 {
		return []string{"worker-leg-default"}
	}
	return append([]string(nil), def.PlaybookIDs...)
}

func (m *PlaybookMatcher) mergePlaybooks(playbookIDs []string, topologyPattern, phaseID string) ([]string, error) {
	topology := strings.TrimSpace(topologyPattern)
	if topology == "" {
		topology = "*"
	}
	phase := strings.TrimSpace(phaseID)
	if phase == "" {
		phase = "*"
	}

	var out []string
	seen := make(map[string]struct{})
	appendItems := func(items []string) {
		for _, item := range items {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}

	if m.fallback != nil {
		appendItems(m.expandChecklist(*m.fallback))
	}

	for _, id := range playbookIDs {
		id = strings.TrimSpace(id)
		if id == "" || (m.fallback != nil && id == m.fallback.ID) {
			continue
		}
		pb, ok := m.byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown playbook %q", id)
		}
		if pb.Fallback {
			continue
		}
		if !playbookTriggersMatch(pb.Triggers, topology, phase) {
			continue
		}
		if strings.TrimSpace(pb.Extends) != "" {
			appendItems(pb.Checklist)
		} else {
			appendItems(m.expandChecklist(pb))
		}
	}
	return out, nil
}

func (m *PlaybookMatcher) expandChecklist(pb Playbook) []string {
	if ext := strings.TrimSpace(pb.Extends); ext != "" {
		base, ok := m.byID[ext]
		if !ok {
			return append([]string(nil), pb.Checklist...)
		}
		var merged []string
		merged = append(merged, m.expandChecklist(base)...)
		merged = append(merged, pb.Checklist...)
		return merged
	}
	return append([]string(nil), pb.Checklist...)
}

func playbookTriggersMatch(tr PlaybookTriggers, topology, phase string) bool {
	return triggerFieldMatch(tr.TopologyPatterns, topology) && triggerFieldMatch(tr.PhaseIDs, phase)
}

func triggerFieldMatch(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "*" || strings.EqualFold(p, value) {
			return true
		}
	}
	return false
}
