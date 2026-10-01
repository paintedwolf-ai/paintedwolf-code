// Package agentdef loads catalog agent definitions.
package agentdef

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"gopkg.in/yaml.v3"
)

// UnitPrefix is the catalog unit namespace agent definitions load from.
const UnitPrefix = "agents/"

// Topology roles select agent turn types.
const (
	TopologyRoleCoordinator = "coordinator"
	TopologyRoleWorker      = "worker"
	TopologyRoleGate        = "gate"
)

// Dispatch lanes constrain coordinator dispatch.
const (
	DispatchLaneRead   = "read"
	DispatchLaneWrite  = "write"
	DispatchLaneReview = "review"
	DispatchLanePlan   = "plan"
)

// DispatchLanes lists the closed lane vocabulary.
func DispatchLanes() []string {
	return []string{DispatchLaneRead, DispatchLaneWrite, DispatchLaneReview, DispatchLanePlan}
}

// Profile is a configurable agent role loaded from YAML. An agent names a
// tool_profile, and that profile is the only statement of the tools it holds.
type Profile struct {
	ID                   string
	Name                 string
	Description          string
	ToolProfile          string
	ExecMode             sandbox.ExecMode
	Skills               skills.Selector
	Capabilities         []string
	TopologyRoles        []string
	DispatchLanes        []string
	SystemPromptTemplate string
	Spawn                SpawnConfig
}

// SpawnConfig controls worker spawn misconfiguration codes.
type SpawnConfig struct {
	MisconfigureCode string
}

type agentFile struct {
	ID                   string           `yaml:"id"`
	Name                 string           `yaml:"name"`
	Description          string           `yaml:"description"`
	ToolProfile          string           `yaml:"tool_profile"`
	ExecMode             sandbox.ExecMode `yaml:"exec_mode"`
	Skills               skills.Selector  `yaml:"skills"`
	Capabilities         []string         `yaml:"capabilities"`
	TopologyRoles        []string         `yaml:"topology_roles"`
	DispatchLanes        []string         `yaml:"dispatch_lanes"`
	SystemPromptTemplate string           `yaml:"system_prompt_template"`
	Spawn                spawnFile        `yaml:"spawn"`
}

type spawnFile struct {
	MisconfigureCode string `yaml:"misconfigure_code"`
}

// Parse parses one agent YAML file. Decoding is strict: an unknown key fails
// the load.
func Parse(data []byte) (Profile, error) {
	var raw agentFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return Profile{}, err
	}
	if raw.ID == "" {
		return Profile{}, fmt.Errorf("agent profile missing id")
	}
	if raw.ExecMode == "" {
		raw.ExecMode = sandbox.ExecModeNone
	}
	if err := validateClosed(raw.ID, "dispatch_lanes", raw.DispatchLanes, DispatchLanes()); err != nil {
		return Profile{}, err
	}
	if err := validateClosed(raw.ID, "capabilities", raw.Capabilities, Capabilities()); err != nil {
		return Profile{}, err
	}
	return Profile{
		ID:                   raw.ID,
		Name:                 raw.Name,
		Description:          raw.Description,
		ToolProfile:          raw.ToolProfile,
		ExecMode:             raw.ExecMode,
		Skills:               raw.Skills,
		Capabilities:         raw.Capabilities,
		TopologyRoles:        raw.TopologyRoles,
		DispatchLanes:        raw.DispatchLanes,
		SystemPromptTemplate: raw.SystemPromptTemplate,
		Spawn:                SpawnConfig{MisconfigureCode: raw.Spawn.MisconfigureCode},
	}, nil
}

// LoadDir loads YAML profiles from an unpackaged directory.
func LoadDir(dir extpacks.Source) ([]Profile, error) {
	entries, err := dir.List()
	if err != nil {
		return nil, err
	}
	var profiles []Profile
	seen := make(map[string]struct{})
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		data, err := dir.Join(e.Name()).Read()
		if err != nil {
			return nil, err
		}
		p, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if _, dup := seen[p.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate agent id %q", e.Name(), p.ID)
		}
		seen[p.ID] = struct{}{}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// LoadEffective loads profiles from the process catalog.
func LoadEffective() ([]Profile, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	return LoadEffectiveWithCatalog(catalog)
}

// LoadEffectiveWithCatalog loads captured winning agent units.
func LoadEffectiveWithCatalog(catalog *extpacks.EffectiveCatalog) ([]Profile, error) {
	if catalog == nil {
		return nil, fmt.Errorf("agent profiles: effective catalog required")
	}
	var profiles []Profile
	seen := make(map[string]struct{})
	for _, id := range catalog.LoadedUnitIDs() {
		if !strings.HasPrefix(id, UnitPrefix) {
			continue
		}
		at, _ := catalog.UnitPath(id)
		// Only YAML units define profiles.
		if !strings.HasSuffix(at.String(), ".yaml") {
			continue
		}
		content, _, ok := catalog.UnitContent(id)
		if !ok {
			continue
		}
		p, err := Parse(content)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		// A duplicate body ID means its unit ID differs.
		if _, dup := seen[p.ID]; dup {
			return nil, fmt.Errorf("duplicate agent id %q across packs", p.ID)
		}
		seen[p.ID] = struct{}{}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// Capabilities an agent may declare; dispatch classification reads them from
// the registry.
const (
	CapabilityResearch   = "research"
	CapabilityExplore    = "explore"
	CapabilitySurvey     = "survey"
	CapabilityReview     = "review"
	CapabilityPlanReview = "plan_review"
	CapabilitySecurity   = "security"
	CapabilityMetadata   = "metadata"
	CapabilityExternal   = "external"
	CapabilityPlan       = "plan"
	CapabilityWrite      = "write"
)

// Capabilities lists the closed capability vocabulary. A value outside it is a
// load fault.
func Capabilities() []string {
	return []string{
		CapabilityResearch, CapabilityExplore, CapabilitySurvey,
		CapabilityReview, CapabilityPlanReview, CapabilitySecurity,
		CapabilityMetadata, CapabilityExternal, CapabilityPlan, CapabilityWrite,
	}
}

// validateClosed rejects a declared value outside its vocabulary, naming the
// field and the allowed values.
func validateClosed(agentID, field string, declared, allowed []string) error {
	for _, value := range declared {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("agent %q: %s has an empty entry", agentID, field)
		}
		if !slices.Contains(allowed, value) {
			return fmt.Errorf("agent %q: %s %q is not one of %s",
				agentID, field, value, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// DiscoveryCapabilities mark an agent that gathers facts.
func DiscoveryCapabilities() []string {
	return []string{CapabilityResearch, CapabilityExplore, CapabilitySurvey}
}

// CritiqueCapabilities mark an agent that judges someone else's work.
func CritiqueCapabilities() []string {
	return []string{CapabilityReview, CapabilityPlanReview}
}

// ToolProfileFor resolves one agent's tool profile from the process catalog.
func ToolProfileFor(agentID string) (string, bool) {
	p, ok := lookup(agentID)
	if !ok {
		return "", false
	}
	return p.ToolProfile, true
}

// SystemPromptTemplateFor resolves one agent's template ref from the process catalog.
func SystemPromptTemplateFor(agentID string) (string, bool) {
	p, ok := lookup(agentID)
	if !ok {
		return "", false
	}
	return p.SystemPromptTemplate, true
}

// IsWorker reports whether agentID declares the worker topology role; only
// workers are task() targets. An agent the catalog does not know is not a worker.
func IsWorker(agentID string) bool {
	p, ok := lookup(agentID)
	if !ok {
		return false
	}
	return slices.Contains(p.TopologyRoles, TopologyRoleWorker)
}

// LanesFor returns the dispatch lanes agentID declares, empty when it declares
// none or the catalog does not know it.
func LanesFor(agentID string) []string {
	p, ok := lookup(agentID)
	if !ok {
		return nil
	}
	return append([]string(nil), p.DispatchLanes...)
}

// ServesLane reports whether agentID may be dispatched on a lane-gated surface.
// An agent the catalog does not know, and one declaring no lane, serves every
// lane.
func ServesLane(agentID, lane string) bool {
	p, ok := lookup(agentID)
	if !ok || len(p.DispatchLanes) == 0 {
		return true
	}
	return slices.Contains(p.DispatchLanes, strings.TrimSpace(lane))
}

// DeclaresAny reports whether agentID declares one of the given capabilities.
func DeclaresAny(agentID string, capabilities ...string) bool {
	p, ok := lookup(agentID)
	if !ok {
		return false
	}
	for _, declared := range p.Capabilities {
		declared = strings.TrimSpace(declared)
		for _, want := range capabilities {
			if declared == want {
				return true
			}
		}
	}
	return false
}

// FilterExternalSourceAgents drops agents that work from external sources, for
// callers narrowing a roster while web search is off.
func FilterExternalSourceAgents(agents []string) []string {
	out := make([]string, 0, len(agents))
	for _, id := range agents {
		if !DeclaresAny(id, CapabilityExternal) {
			out = append(out, id)
		}
	}
	return out
}

func lookup(agentID string) (Profile, bool) {
	agentID = strings.TrimSpace(agentID)
	profiles, err := LoadEffective()
	if err != nil {
		return Profile{}, false
	}
	for _, p := range profiles {
		if p.ID == agentID {
			return p, true
		}
	}
	return Profile{}, false
}
