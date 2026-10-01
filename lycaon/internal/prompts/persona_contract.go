package prompts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
)

type PersonaContract struct {
	Version                  int                        `yaml:"version"`
	RequiredHeadingsRendered []string                   `yaml:"required_headings_rendered"`
	Archetypes               map[string]ArchetypeDef    `yaml:"archetypes"`
	Agents                   map[string]AgentPersonaDef `yaml:"agents"`
}

// ArchetypeDef defines shared partials for a persona archetype.
type ArchetypeDef struct {
	Shell            string   `yaml:"shell"`
	RequiredPartials []string `yaml:"required_partials"`
}

// AgentPersonaDef defines prompt-only persona fields.
type AgentPersonaDef struct {
	Archetype              string       `yaml:"archetype"`
	Advisory               bool         `yaml:"advisory,omitempty"`
	PlaybookIDs            []string     `yaml:"playbook_ids"`
	MustSubstringsRendered []string     `yaml:"must_substrings_rendered"`
	Delta                  PersonaDelta `yaml:"delta"`
}

// PersonaDelta supplies an agent's persona template variables.
type PersonaDelta struct {
	RoleTitle string   `yaml:"role_title"`
	Focus     string   `yaml:"focus"`
	Process   []string `yaml:"process"`
}

// LoadPersonaContract merges the persona contract across contributing packs.
func LoadPersonaContract() (*PersonaContract, error) {
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, fmt.Errorf("persona contract: %w", err)
	}
	return LoadPersonaContractWithCatalog(eff)
}

// LoadPersonaContractWithCatalog merges persona rows from contributing packs.
func LoadPersonaContractWithCatalog(eff *extpacks.EffectiveCatalog) (*PersonaContract, error) {
	packs, err := extpacks.DiscoverEffective(eff)
	if err != nil {
		return nil, fmt.Errorf("persona contract: %w", err)
	}
	merged := PersonaContract{
		Archetypes: map[string]ArchetypeDef{},
		Agents:     map[string]AgentPersonaDef{},
	}
	headingsFrom := ""
	archetypeFrom := map[string]string{}
	agentFrom := map[string]string{}
	for _, p := range packs {
		at := p.Root.Join("agents", "prompts", config.PersonaContractFile)
		if _, statErr := at.Stat(); statErr != nil {
			continue
		}
		raw, err := at.Read()
		if err != nil {
			return nil, fmt.Errorf("persona contract %s: %w", p.ID, err)
		}
		var cfg PersonaContract
		if err := config.DecodeYAML(raw, &cfg); err != nil {
			return nil, fmt.Errorf("persona contract %s: %w", p.ID, err)
		}
		if cfg.Version < 1 {
			return nil, fmt.Errorf("persona contract %s: missing or invalid version", p.ID)
		}
		if merged.Version == 0 {
			merged.Version = cfg.Version
		} else if cfg.Version != merged.Version {
			return nil, fmt.Errorf("persona contract %s: version %d does not match version %d already loaded",
				p.ID, cfg.Version, merged.Version)
		}
		if len(cfg.RequiredHeadingsRendered) > 0 {
			if headingsFrom != "" {
				return nil, fmt.Errorf("persona contract %s: required_headings_rendered already declared by %s",
					p.ID, headingsFrom)
			}
			headingsFrom = p.ID
			merged.RequiredHeadingsRendered = cfg.RequiredHeadingsRendered
		}
		for id, def := range cfg.Archetypes {
			if prev, dup := archetypeFrom[id]; dup {
				return nil, fmt.Errorf("persona contract: archetype %q declared by %s and %s", id, prev, p.ID)
			}
			archetypeFrom[id] = p.ID
			merged.Archetypes[id] = def
		}
		for id, def := range cfg.Agents {
			if prev, dup := agentFrom[id]; dup {
				return nil, fmt.Errorf("persona contract: agent %q declared by %s and %s", id, prev, p.ID)
			}
			agentFrom[id] = p.ID
			merged.Agents[id] = def
		}
	}
	if merged.Version < 1 {
		return nil, fmt.Errorf("persona contract: no contributing pack declares one")
	}
	if len(merged.Agents) == 0 {
		return nil, fmt.Errorf("persona contract: no agents defined")
	}
	if err := validateArchetypeRefs(merged, agentFrom); err != nil {
		return nil, err
	}
	return &merged, nil
}

// validateArchetypeRefs requires every agent archetype to exist.
func validateArchetypeRefs(merged PersonaContract, agentFrom map[string]string) error {
	var problems []string
	for id, def := range merged.Agents {
		arch := strings.TrimSpace(def.Archetype)
		if arch == "" {
			problems = append(problems, fmt.Sprintf("agent %q (from %s) declares no archetype", id, agentFrom[id]))
			continue
		}
		if _, ok := merged.Archetypes[arch]; !ok {
			problems = append(problems, fmt.Sprintf(
				"agent %q (from %s) names archetype %q, which no contributing pack declares",
				id, agentFrom[id], arch))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("persona contract: %s", strings.Join(problems, "; "))
}

// WorkerAgentIDs returns sorted agent ids from the contract (excludes coordinator).
func (c *PersonaContract) WorkerAgentIDs() []string {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.Agents))
	for id := range c.Agents {
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

// IsWorkerAgent reports whether agentID is defined in the persona contract.
func (c *PersonaContract) IsWorkerAgent(agentID string) bool {
	if c == nil {
		return false
	}
	_, ok := c.Agents[strings.TrimSpace(agentID)]
	return ok
}

// AgentPersonaArchetype resolves a worker archetype.
func AgentPersonaArchetype(agentType string) (archetype string, ok bool) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return "", false
	}
	// Cached: the load walks every pack, and this runs per dispatch.
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		return "", false
	}
	cfg, err := loadPersonaContractCached(eff)
	if err != nil {
		return "", false
	}
	def, found := cfg.Agents[agentType]
	if !found {
		return "", false
	}
	arch := strings.TrimSpace(def.Archetype)
	if arch == "" {
		return "", false
	}
	return arch, true
}

// AgentIsReadScout reports read-only exploration workers.
func AgentIsReadScout(agentType string) bool {
	arch, ok := AgentPersonaArchetype(agentType)
	return ok && arch == "explore_readonly"
}

// AgentIsWebResearcher reports workers whose tool profile is web_research.
func AgentIsWebResearcher(agentType string) bool {
	profile, err := ToolProfileForAgent(agentType)
	return err == nil && profile == "web_research"
}

// ValidatePersonaContractCoverage requires one row per runnable agent.
func ValidatePersonaContractCoverage(cfg *PersonaContract, agentIDs []string) error {
	if cfg == nil {
		return fmt.Errorf("persona contract: required")
	}
	var missing []string
	for _, id := range agentIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := cfg.Agents[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("persona contract: no row for agent(s) %s", strings.Join(missing, ", "))
}

// PersonaDataMap builds render variables from an agent contract row.
func PersonaDataMap(def AgentPersonaDef) map[string]any {
	process := formatProcessList(def.Delta.Process)
	return map[string]any{
		"role_title": strings.TrimSpace(def.Delta.RoleTitle),
		"focus":      strings.TrimSpace(def.Delta.Focus),
		"process":    process,
	}
}

func formatProcessList(steps []string) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	for i, step := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(step))
	}
	return strings.TrimRight(b.String(), "\n")
}
