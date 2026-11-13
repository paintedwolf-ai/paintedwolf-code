package oar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The standard fixes eight core anchor ids. The host's anchor profile map
// binds each to the local anchor that implements it ([OAR-PROF-2]).

// Core anchor ids, frozen by the specification in spec order.
const (
	CoreAnchorToolPreInvoke   = "tool.pre_invoke"
	CoreAnchorToolHandler     = "tool.handler"
	CoreAnchorToolPostInvoke  = "tool.post_invoke"
	CoreAnchorAgentPostTurn   = "agent.post_turn"
	CoreAnchorAgentFinalize   = "agent.finalize"
	CoreAnchorModelInput      = "model.input"
	CoreAnchorModelOutput     = "model.output"
	CoreAnchorModelToolResult = "model.tool_result"
)

var coreAnchors = []string{
	CoreAnchorToolPreInvoke,
	CoreAnchorToolHandler,
	CoreAnchorToolPostInvoke,
	CoreAnchorAgentPostTurn,
	CoreAnchorAgentFinalize,
	CoreAnchorModelInput,
	CoreAnchorModelOutput,
	CoreAnchorModelToolResult,
}

// CoreAnchors returns the frozen normative anchor set in spec order.
func CoreAnchors() []string {
	return append([]string(nil), coreAnchors...)
}

// IsCoreAnchor reports whether id is one of the eight core anchor ids.
func IsCoreAnchor(id string) bool {
	for _, a := range coreAnchors {
		if a == id {
			return true
		}
	}
	return false
}

// EngineDeclaration is everything this engine declares about itself: the
// published anchor profile map, the standard profiles it claims, its host
// anchors and host-tier facts, the detectors it resolves, and its limits. The
// profile map is the normative document ([OAR-PROF-2]); the rest is this
// host's own convention for declaring the closed fact environment before any
// rule compiles ([OAR-FACT-1]).
type EngineDeclaration struct {
	ProfileMap  AnchorProfileMap `yaml:"profile_map" json:"profile_map"`
	Host        string           `yaml:"host" json:"host"`
	HostAnchors []string         `yaml:"host_anchors,omitempty" json:"host_anchors,omitempty"`
	Profiles    []string         `yaml:"profiles" json:"profiles"`
	Window      int              `yaml:"activity_window" json:"activity_window"`
	HostFacts   []HostFactDecl   `yaml:"host_facts,omitempty" json:"host_facts,omitempty"`
	Detectors   []string         `yaml:"detectors" json:"detectors"`

	ExpressionNodesMax int  `yaml:"expression_nodes_max" json:"expression_nodes_max"`
	SupportsTransform  bool `yaml:"supports_transform" json:"supports_transform"`
	SupportsChunk      bool `yaml:"supports_chunk" json:"supports_chunk"`
}

// AnchorProfileMap is the published anchor profile map
// (oar-profile.schema.json). Every core anchor appears exactly once across
// core and unsupported.
type AnchorProfileMap struct {
	Version     string            `yaml:"oar_profile_version" json:"oar_profile_version"`
	Core        map[string]string `yaml:"core" json:"core"`
	Unsupported []string          `yaml:"unsupported" json:"unsupported"`
}

// HostFactDecl is one host-tier observation, published under this host's
// namespace ([OAR-FACT-15]).
type HostFactDecl struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"`
	Observation string `yaml:"observation,omitempty" json:"observation,omitempty"`
}

// ParseEngineDeclaration decodes and validates a declaration, including that
// every mapped local anchor is installed.
func ParseEngineDeclaration(raw []byte) (*EngineDeclaration, error) {
	d, err := ParseEngineDeclarationShape(raw)
	if err != nil {
		return nil, err
	}
	for _, local := range d.HostAnchors {
		if anchorcatalog.Loaded() && anchorcatalog.Require(local) != nil {
			return nil, fmt.Errorf("[OAR-PROF-4] host anchor %q is not installed", local)
		}
	}
	for id, local := range d.ProfileMap.Core {
		if anchorcatalog.Loaded() && anchorcatalog.Require(local) != nil {
			return nil, fmt.Errorf("core anchor %q maps to %q, which is not an installed catalog anchor", id, local)
		}
	}
	return d, nil
}

// ParseEngineDeclarationShape validates a declaration without resolving
// local anchors against the installed catalog.
func ParseEngineDeclarationShape(raw []byte) (*EngineDeclaration, error) {
	var d EngineDeclaration
	if err := config.DecodeYAML(raw, &d); err != nil {
		return nil, fmt.Errorf("oar engine declaration: %w", err)
	}
	if _, err := d.Environment(); err != nil {
		return nil, err
	}
	return &d, nil
}

// LoadEngineDeclaration reads and validates a declaration file.
func LoadEngineDeclaration(path string) (*EngineDeclaration, error) {
	raw, err := os.ReadFile(path) // #nosec G304 — host config path supplied by the caller.
	if err != nil {
		return nil, fmt.Errorf("oar engine declaration: read %s: %w", path, err)
	}
	return ParseEngineDeclaration(raw)
}

// profileMapDocument is the anchor profile map as the published document.
func (d *EngineDeclaration) profileMapDocument() map[string]any {
	core := map[string]any{}
	for k, v := range d.ProfileMap.Core {
		core[k] = v
	}
	unsupported := make([]any, 0, len(d.ProfileMap.Unsupported))
	for _, u := range d.ProfileMap.Unsupported {
		unsupported = append(unsupported, u)
	}
	return map[string]any{"oar_profile_version": d.ProfileMap.Version, "core": core, "unsupported": unsupported}
}

// Environment declares the closed fact environment rules compile against.
func (d *EngineDeclaration) Environment() (*oarcore.Environment, error) {
	if d == nil {
		return nil, fmt.Errorf("oar engine declaration required")
	}
	profileMap, err := oarcore.LoadProfileMap(d.profileMapDocument())
	if err != nil {
		return nil, err
	}
	facts := make(map[string]string, len(d.HostFacts))
	for _, f := range d.HostFacts {
		if _, dup := facts[f.Name]; dup {
			return nil, fmt.Errorf("[OAR-FACT-19] host fact %s is declared twice", f.Name)
		}
		facts[f.Name] = strings.TrimSpace(f.Type)
	}
	return oarcore.NewEnvironment(oarcore.EnvironmentSpec{
		Host: d.Host, ProfileMap: profileMap, HostAnchors: d.HostAnchors, Profiles: d.Profiles,
		HostFacts: facts, ActivityWindow: d.Window, Detectors: d.Detectors,
		ExpressionNodesMax: d.ExpressionNodesMax, SupportsTransform: d.SupportsTransform, SupportsChunk: d.SupportsChunk,
	})
}

// Supports reports whether the host implements a core anchor.
func (d *EngineDeclaration) Supports(core string) bool {
	if d == nil {
		return false
	}
	_, ok := d.ProfileMap.Core[core]
	return ok
}

// ResolveAnchor maps a core anchor id to the local catalog anchor
// ([OAR-PROF-3]). A host-native id is returned unchanged when declared
// ([OAR-PROF-4]).
func (d *EngineDeclaration) ResolveAnchor(on string) (string, error) {
	on = strings.TrimSpace(on)
	if !IsCoreAnchor(on) {
		if d != nil {
			for _, anchor := range d.HostAnchors {
				if anchor == on {
					return on, nil
				}
			}
		}
		return "", fmt.Errorf("[OAR-SEL-1] anchor %q is not declared", on)
	}
	if d == nil {
		return "", fmt.Errorf("core anchor %q requires an anchor profile map", on)
	}
	if local, ok := d.ProfileMap.Core[on]; ok {
		return local, nil
	}
	return "", fmt.Errorf("[OAR-PROF-3] core anchor %q is unsupported", on)
}

// CoreAnchorFor returns the core anchor a local catalog anchor implements, or
// "" when the local anchor is host-native.
func (d *EngineDeclaration) CoreAnchorFor(local string) string {
	if d == nil {
		return ""
	}
	for core, l := range d.ProfileMap.Core {
		if l == local {
			return core
		}
	}
	return ""
}

// UnsupportedCoreAnchors returns the declared-unsupported ids, sorted.
func (d *EngineDeclaration) UnsupportedCoreAnchors() []string {
	if d == nil {
		return nil
	}
	out := append([]string(nil), d.ProfileMap.Unsupported...)
	sort.Strings(out)
	return out
}

var installedDeclaration atomic.Pointer[EngineDeclaration]

// InstallEngineDeclaration publishes the process declaration.
func InstallEngineDeclaration(d *EngineDeclaration) {
	installedDeclaration.Store(d)
}

// InstallEngineDeclarationFile loads path and installs it.
func InstallEngineDeclarationFile(path string) error {
	d, err := LoadEngineDeclaration(path)
	if err != nil {
		return err
	}
	InstallEngineDeclaration(d)
	return nil
}

// InstalledEngineDeclaration returns the installed or bundled declaration.
func InstalledEngineDeclaration() *EngineDeclaration {
	if d := installedDeclaration.Load(); d != nil {
		return d
	}
	shippedOnce.Do(func() {
		raw, err := config.Read(config.OARProfile)
		if err != nil {
			return
		}
		d, err := ParseEngineDeclaration(raw)
		if err != nil {
			return
		}
		installedDeclaration.CompareAndSwap(nil, d)
	})
	return installedDeclaration.Load()
}

var shippedOnce sync.Once

// ResolveRuleAnchor resolves a rule's anchor through the installed profile
// map. Host-native ids pass through; a core anchor the host does not support
// is refused ([OAR-PROF-3]).
func ResolveRuleAnchor(ruleID, on string) (string, error) {
	return resolveRuleAnchorWith(InstalledEngineDeclaration(), ruleID, on)
}

func resolveRuleAnchorWith(d *EngineDeclaration, ruleID, on string) (string, error) {
	on = strings.TrimSpace(on)
	if !IsCoreAnchor(on) {
		return on, nil
	}
	local, err := d.ResolveAnchor(on)
	if err != nil {
		return "", fmt.Errorf("[OAR-PROF-3] rule %q targets core anchor %q, which this host does not support", ruleID, on)
	}
	return local, nil
}

// ValidateProfileMap checks the declaration's anchor profile map against the
// vendored oar-profile.schema.json. An empty schemaDir skips validation.
func ValidateProfileMap(schemaDir string, raw []byte) error {
	if strings.TrimSpace(schemaDir) == "" {
		return nil
	}
	path := filepath.Join(schemaDir, "oar", "oar-profile.schema.json")
	schemaRaw, err := os.ReadFile(path) // #nosec G304 — repo schema path.
	if err != nil {
		return fmt.Errorf("oar profile map: read vendored schema %s: %w (run ./task oar:vendor)", path, err)
	}
	var schemaDoc any
	if err := json.Unmarshal(schemaRaw, &schemaDoc); err != nil {
		return fmt.Errorf("oar profile map: parse %s: %w", path, err)
	}
	compiler := jsonschema.NewCompiler()
	const id = "oar-profile.schema.json"
	if err := compiler.AddResource(id, schemaDoc); err != nil {
		return fmt.Errorf("oar profile map: %w", err)
	}
	schema, err := compiler.Compile(id)
	if err != nil {
		return fmt.Errorf("oar profile map: compile %s: %w", id, err)
	}
	var d EngineDeclaration
	if err := config.DecodeYAML(raw, &d); err != nil {
		return fmt.Errorf("oar profile map: %w", err)
	}
	encoded, err := json.Marshal(d.profileMapDocument())
	if err != nil {
		return fmt.Errorf("oar profile map: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("oar profile map: %w", err)
	}
	if err := schema.Validate(inst); err != nil {
		return fmt.Errorf("oar profile map: oar-profile.schema.json: %w", err)
	}
	return nil
}

// InstallEngineDeclarationBundled validates and installs the bundled
// declaration.
func InstallEngineDeclarationBundled(schemaDir string) error {
	raw, err := config.Read(config.OARProfile)
	if err != nil {
		return fmt.Errorf("oar engine declaration: read bundled declaration: %w", err)
	}
	return installDeclarationRaw(schemaDir, raw)
}

func installDeclarationRaw(schemaDir string, raw []byte) error {
	if err := ValidateProfileMap(schemaDir, raw); err != nil {
		return err
	}
	d, err := ParseEngineDeclaration(raw)
	if err != nil {
		return err
	}
	if err := d.requireProfilesMatchCatalogue(); err != nil {
		return err
	}
	if err := d.requireHostAdapters(); err != nil {
		return err
	}
	InstallEngineDeclaration(d)
	return nil
}

// requireProfilesMatchCatalogue checks claimed profiles against the facts the
// engine provides: a claimed profile is provided whole, and no fact under an
// unclaimed profile's name is provided ([OAR-FACT-13]).
func (d *EngineDeclaration) requireProfilesMatchCatalogue() error {
	claimed := map[string]bool{}
	for _, name := range d.Profiles {
		claimed[strings.TrimSpace(name)] = true
	}
	for _, name := range StandardProfiles() {
		if !claimed[name] {
			return fmt.Errorf("[OAR-FACT-13] this engine provides facts of profile %q but the declaration does not claim it", name)
		}
		delete(claimed, name)
	}
	for name := range claimed {
		return fmt.Errorf("[OAR-FACT-13] the declaration claims profile %q, which this engine provides no facts for", name)
	}
	return nil
}
