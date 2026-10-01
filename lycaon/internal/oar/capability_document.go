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

// The standard fixes eight core anchor ids. The capability document maps each
// supported core anchor to a local catalog anchor. Rules authored against core
// anchors are the portable subset.

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

// CapabilityDocument declares the core anchors, profiles, host facts, and
// evaluation limits this host implements ([OAR-FACT-21]).
type CapabilityDocument struct {
	Version   string         `yaml:"oar_capability_version" json:"oar_capability_version"`
	Host      string         `yaml:"host,omitempty" json:"host,omitempty"`
	Anchors   AnchorMap      `yaml:"anchors" json:"anchors"`
	Profiles  []string       `yaml:"profiles" json:"profiles"`
	Window    int            `yaml:"activity_window" json:"activity_window"`
	HostFacts []HostFactDecl `yaml:"host_facts,omitempty" json:"host_facts,omitempty"`
	Detectors []string       `yaml:"detectors" json:"detectors"`

	ExpressionNodesMax int  `yaml:"expression_nodes_max" json:"expression_nodes_max"`
	SupportsTransform  bool `yaml:"supports_transform" json:"supports_transform"`

	unsupported map[string]struct{}
}

// AnchorMap is the capability document's anchors section. Every core anchor
// appears exactly once across the two members ([OAR-PROF-2]).
type AnchorMap struct {
	Core        map[string]string `yaml:"core" json:"core"`
	Unsupported []string          `yaml:"unsupported,omitempty" json:"unsupported,omitempty"`
	Host        []string          `yaml:"host,omitempty" json:"host,omitempty"`
}

// HostFactDecl is one host-tier observation, published under a namespace this
// host defines ([OAR-FACT-18]).
type HostFactDecl struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"`
	Observation string `yaml:"observation,omitempty" json:"observation,omitempty"`
}

// ParseCapabilityDocument decodes and validates a capability document.
func ParseCapabilityDocument(raw []byte) (*CapabilityDocument, error) {
	var document map[string]any
	if err := config.DecodeYAML(raw, &document); err != nil {
		return nil, err
	}
	if _, err := oarcore.LoadCapability(document); err != nil {
		return nil, err
	}
	var p CapabilityDocument
	if err := config.DecodeYAML(raw, &p); err != nil {
		return nil, fmt.Errorf("oar profile: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// ParseCapabilityDocumentShape validates shape without resolving local ids.
func ParseCapabilityDocumentShape(raw []byte) (*CapabilityDocument, error) {
	var document map[string]any
	if err := config.DecodeYAML(raw, &document); err != nil {
		return nil, err
	}
	if _, err := oarcore.LoadCapability(document); err != nil {
		return nil, err
	}
	var p CapabilityDocument
	if err := config.DecodeYAML(raw, &p); err != nil {
		return nil, fmt.Errorf("oar profile: %w", err)
	}
	if err := p.validateShape(); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadCapabilityDocument reads and validates a capability document.
func LoadCapabilityDocument(path string) (*CapabilityDocument, error) {
	raw, err := os.ReadFile(path) // #nosec G304 — host config path supplied by the caller.
	if err != nil {
		return nil, fmt.Errorf("oar profile: read %s: %w", path, err)
	}
	return ParseCapabilityDocument(raw)
}

// validate is the host's own profile check: shape, plus that every mapped
// local id is an installed catalog anchor.
func (p *CapabilityDocument) validate() error {
	if err := p.validateShape(); err != nil {
		return err
	}
	for _, local := range p.Anchors.Host {
		if anchorcatalog.Loaded() && anchorcatalog.Require(local) != nil {
			return fmt.Errorf("[OAR-PROF-5] host anchor %q is not installed", local)
		}
	}
	for id, local := range p.Anchors.Core {
		// Membership is only checkable once the host catalog is installed.
		if anchorcatalog.Loaded() && anchorcatalog.Require(local) != nil {
			return fmt.Errorf("core anchor %q maps to %q, which is not an installed catalog anchor", id, local)
		}
	}
	return nil
}

// validateShape checks totality and vocabulary without resolving local ids.
func (p *CapabilityDocument) validateShape() error {
	if _, err := capabilityEnvironment(p); err != nil {
		return err
	}
	p.unsupported = make(map[string]struct{}, len(p.Anchors.Unsupported))
	for _, id := range p.Anchors.Unsupported {
		p.unsupported[id] = struct{}{}
	}
	return nil
}

// Supports reports whether the host implements a core anchor.
func (p *CapabilityDocument) Supports(core string) bool {
	if p == nil {
		return false
	}
	_, ok := p.Anchors.Core[core]
	return ok
}

// ResolveAnchor maps a core anchor id to the local catalog anchor. A
// host-native id is returned unchanged.
func (p *CapabilityDocument) ResolveAnchor(on string) (string, error) {
	on = strings.TrimSpace(on)
	if !IsCoreAnchor(on) {
		if p != nil {
			for _, anchor := range p.Anchors.Host {
				if anchor == on {
					return on, nil
				}
			}
		}
		return "", fmt.Errorf("[OAR-PROF-5] host anchor %q is not declared", on)
	}
	if p == nil {
		return "", fmt.Errorf("core anchor %q requires a host capability document", on)
	}
	if local, ok := p.Anchors.Core[on]; ok {
		return local, nil
	}
	return "", fmt.Errorf("core anchor %q is not supported", on)
}

// CoreAnchorFor returns the core anchor a local catalog anchor implements, or
// "" when the local anchor is host-native.
func (p *CapabilityDocument) CoreAnchorFor(local string) string {
	if p == nil {
		return ""
	}
	for core, l := range p.Anchors.Core {
		if l == local {
			return core
		}
	}
	return ""
}

// UnsupportedCoreAnchors returns the declared-unsupported ids, sorted.
func (p *CapabilityDocument) UnsupportedCoreAnchors() []string {
	if p == nil {
		return nil
	}
	out := append([]string(nil), p.Anchors.Unsupported...)
	sort.Strings(out)
	return out
}

var installedCapability atomic.Pointer[CapabilityDocument]

// InstallCapabilityDocument publishes the process capability document.
func InstallCapabilityDocument(p *CapabilityDocument) {
	installedCapability.Store(p)
}

// InstallCapabilityDocumentFile loads path and installs it.
func InstallCapabilityDocumentFile(path string) error {
	p, err := LoadCapabilityDocument(path)
	if err != nil {
		return err
	}
	InstallCapabilityDocument(p)
	return nil
}

// InstalledCapabilityDocument returns the installed or bundled document.
func InstalledCapabilityDocument() *CapabilityDocument {
	if p := installedCapability.Load(); p != nil {
		return p
	}
	shippedOnce.Do(func() {
		raw, err := config.Read(config.OARProfile)
		if err != nil {
			return
		}
		p, err := ParseCapabilityDocument(raw)
		if err != nil {
			return
		}
		installedCapability.CompareAndSwap(nil, p)
	})
	return installedCapability.Load()
}

var shippedOnce sync.Once

// ResolveRuleAnchor resolves a rule's anchor through the installed capability document.
// Host-native ids pass through without a document; portable core anchors fail
// closed when the host has not published their mapping.
func ResolveRuleAnchor(ruleID, on string) (string, error) {
	return resolveRuleAnchorWith(InstalledCapabilityDocument(), ruleID, on)
}

func resolveRuleAnchorWith(p *CapabilityDocument, ruleID, on string) (string, error) {
	on = strings.TrimSpace(on)
	if !IsCoreAnchor(on) {
		return on, nil
	}
	local, err := p.ResolveAnchor(on)
	if err != nil {
		return "", fmt.Errorf("rule %q targets core anchor %q, which this host does not support", ruleID, on)
	}
	return local, nil
}

// ValidateCapabilityDocument checks raw against the capability schema.
// An empty schemaDir skips schema validation.
func ValidateCapabilityDocument(schemaDir string, raw []byte) error {
	if strings.TrimSpace(schemaDir) == "" {
		return nil
	}
	path := filepath.Join(schemaDir, "oar", "oar-capability.schema.json")
	schemaRaw, err := os.ReadFile(path) // #nosec G304 — repo schema path.
	if err != nil {
		return fmt.Errorf("oar capability: read vendored schema %s: %w "+
			"(run ./task oar:vendor to fetch it from openagentrules.org)", path, err)
	}
	var schemaDoc any
	if err := json.Unmarshal(schemaRaw, &schemaDoc); err != nil {
		return fmt.Errorf("oar capability: parse %s: %w", path, err)
	}
	compiler := jsonschema.NewCompiler()
	const id = "oar-capability.schema.json"
	if err := compiler.AddResource(id, schemaDoc); err != nil {
		return fmt.Errorf("oar capability: %w", err)
	}
	schema, err := compiler.Compile(id)
	if err != nil {
		return fmt.Errorf("oar capability: compile %s: %w", id, err)
	}
	var doc any
	if err := config.DecodeYAML(raw, &doc); err != nil {
		return fmt.Errorf("oar capability: %w", err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("oar capability: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("oar capability: %w", err)
	}
	if err := schema.Validate(inst); err != nil {
		return fmt.Errorf("oar capability: oar-capability.schema.json: %w", err)
	}
	return nil
}

// InstallCapabilityBundled validates and installs the bundled document.
func InstallCapabilityBundled(schemaDir string) error {
	raw, err := config.Read(config.OARProfile)
	if err != nil {
		return fmt.Errorf("oar capability: read bundled profile: %w", err)
	}
	return installCapabilityRaw(schemaDir, raw)
}

func installCapabilityRaw(schemaDir string, raw []byte) error {
	if err := ValidateCapabilityDocument(schemaDir, raw); err != nil {
		return err
	}
	p, err := ParseCapabilityDocument(raw)
	if err != nil {
		return err
	}
	if err := p.requireProfilesMatchCatalogue(); err != nil {
		return err
	}
	if err := p.requireHostAdapters(); err != nil {
		return err
	}
	InstallCapabilityDocument(p)
	return nil
}

// requireProfilesMatchCatalogue checks claimed profiles against registered facts.
func (p *CapabilityDocument) requireProfilesMatchCatalogue() error {
	claimed := map[string]bool{}
	for _, name := range p.Profiles {
		claimed[strings.TrimSpace(name)] = true
	}
	for _, name := range StandardProfiles() {
		if !claimed[name] {
			return fmt.Errorf(
				"oar capability: this engine declares facts for profile %q but the document does not claim it", name)
		}
		delete(claimed, name)
	}
	for name := range claimed {
		return fmt.Errorf(
			"oar capability: the document claims profile %q, which this engine provides no facts for", name)
	}
	return nil
}
