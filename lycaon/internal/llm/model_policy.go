package llm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

// PoolSelection is how single-agent dispatch picks from the pool.
type PoolSelection string

const (
	PoolSelectionRoundRobin PoolSelection = "round_robin"
	PoolSelectionRandom     PoolSelection = "random"
	PoolSelectionFirst      PoolSelection = "first"
)

// ModelRef is a provider + model pair.
type ModelRef struct {
	ProviderID string `yaml:"provider_id" json:"provider_id"`
	Model      string `yaml:"model" json:"model"`
}

// AgentPool is the shared subagent model pool.
type AgentPool struct {
	Selection PoolSelection `yaml:"selection" json:"selection"`
	Models    []ModelRef    `yaml:"models" json:"models"`
}

// ModelPolicy holds coordinator, lite, and agent pool settings.
type ModelPolicy struct {
	ThinkingOverrides []modelcall.ThinkingOverride `yaml:"thinking_overrides,omitempty" json:"thinking_overrides,omitempty"`
	Coordinator       ModelRef                     `yaml:"coordinator" json:"coordinator"`
	Lite              ModelRef                     `yaml:"lite" json:"lite"`
	AgentPool         AgentPool                    `yaml:"agent_pool" json:"agent_pool"`
}

// PolicyStore merges bundled, global, and project policies.
type PolicyStore struct {
	mu           sync.RWMutex
	globalPath   string
	bundled      ModelPolicy
	global       *ModelPolicy
	projectCache map[string]ModelPolicy
}

// NewPolicyStoreAt loads policy from an explicit global path.
func NewPolicyStoreAt(globalPath string) (*PolicyStore, error) {
	s := &PolicyStore{
		globalPath:   globalPath,
		projectCache: make(map[string]ModelPolicy),
	}
	if err := s.reloadBundled(); err != nil {
		return nil, err
	}
	if err := s.reloadGlobal(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewInMemoryPolicyStore builds a store without disk-backed overlays.
func NewInMemoryPolicyStore(bundled ModelPolicy) *PolicyStore {
	return &PolicyStore{
		bundled:      normalizePolicy(bundled),
		projectCache: make(map[string]ModelPolicy),
	}
}

// NewPolicyStore loads bundled policy and the optional global overlay.
func NewPolicyStore() (*PolicyStore, error) {
	globalPath, err := userModelPolicyPath()
	if err != nil {
		return nil, err
	}
	return NewPolicyStoreAt(globalPath)
}

// decodePolicy parses one named policy layer.
func decodePolicy(data []byte, label string) (ModelPolicy, error) {
	var p ModelPolicy
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&p); err != nil {
		return ModelPolicy{}, fmt.Errorf("parse model policy %s: %w", label, err)
	}
	if err := validatePolicyShape(p); err != nil {
		return ModelPolicy{}, fmt.Errorf("validate model policy %s: %w", label, err)
	}
	return p, nil
}

func validatePolicyShape(p ModelPolicy) error {
	if err := validateThinkingOverrides(p.ThinkingOverrides); err != nil {
		return err
	}
	switch p.AgentPool.Selection {
	case "", PoolSelectionRoundRobin, PoolSelectionRandom, PoolSelectionFirst:
	default:
		return fmt.Errorf("unknown agent_pool.selection %q", p.AgentPool.Selection)
	}
	refs := append([]ModelRef{p.Coordinator, p.Lite}, p.AgentPool.Models...)
	for _, ref := range refs {
		if (ref.ProviderID == "") != (ref.Model == "") {
			return fmt.Errorf("provider_id and model must be set together")
		}
	}
	return nil
}

// loadPolicyOverlay reads a host overlay layer (global or project).
func loadPolicyOverlay(path string) (ModelPolicy, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- user/project settings path
	if err != nil {
		return ModelPolicy{}, err
	}
	return decodePolicy(data, path)
}

func (s *PolicyStore) reloadBundled() error {
	data, err := config.Read(config.ModelPolicy)
	if err != nil {
		return err
	}
	p, err := decodePolicy(data, config.ModelPolicy.String())
	if err != nil {
		return err
	}
	s.bundled = normalizePolicy(p)
	return nil
}

func (s *PolicyStore) reloadGlobal() error {
	p, err := loadPolicyOverlay(s.globalPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.global = nil
			return nil
		}
		return err
	}
	n := normalizePolicy(p)
	s.global = &n
	return nil
}

func cloneModelPolicy(p ModelPolicy) ModelPolicy {
	p.ThinkingOverrides = cloneThinkingOverrides(p.ThinkingOverrides)
	p.AgentPool.Models = slices.Clone(p.AgentPool.Models)
	return p
}

func normalizePolicy(p ModelPolicy) ModelPolicy {
	p = cloneModelPolicy(p)
	if p.AgentPool.Selection == "" {
		p.AgentPool.Selection = PoolSelectionRoundRobin
	}
	return p
}

func modelRefSet(r ModelRef) bool {
	return r.ProviderID != "" || r.Model != ""
}

func mergeModelRef(base, overlay ModelRef) ModelRef {
	if !modelRefSet(overlay) {
		return base
	}
	return overlay
}

// policyHasContent distinguishes an inherited overlay.
func policyHasContent(p ModelPolicy) bool {
	if len(p.ThinkingOverrides) > 0 {
		return true
	}
	if modelRefSet(p.Coordinator) || modelRefSet(p.Lite) {
		return true
	}
	for _, m := range p.AgentPool.Models {
		if modelRefSet(m) {
			return true
		}
	}
	return false
}

// mergePolicy keeps base values for empty overlay slots.
func mergePolicy(base, overlay ModelPolicy) ModelPolicy {
	out := base
	out.ThinkingOverrides = mergeThinkingOverrides(base.ThinkingOverrides, overlay.ThinkingOverrides)
	out.Coordinator = mergeModelRef(base.Coordinator, overlay.Coordinator)
	out.Lite = mergeModelRef(base.Lite, overlay.Lite)
	if overlay.AgentPool.Selection != "" {
		out.AgentPool.Selection = overlay.AgentPool.Selection
	}
	if len(overlay.AgentPool.Models) > 0 {
		out.AgentPool.Models = append([]ModelRef(nil), overlay.AgentPool.Models...)
	}
	return normalizePolicy(out)
}

// CoordinatorRef returns the coordinator assignment.
func CoordinatorRef(p ModelPolicy) ModelRef { return p.Coordinator }

// SummarizerRef returns the lite override when set, otherwise the coordinator default.
func SummarizerRef(p ModelPolicy) ModelRef {
	if ref := p.Lite; ref.ProviderID != "" && ref.Model != "" {
		return ref
	}
	return p.Coordinator
}

// SummarizerSharesCoordinator detects shared provider capacity.
func SummarizerSharesCoordinator(p ModelPolicy) bool {
	sum := SummarizerRef(p)
	coord := p.Coordinator
	if sum.ProviderID == "" || coord.ProviderID == "" {
		return true
	}
	return sum.ProviderID == coord.ProviderID
}

func projectModelPolicyPath(projectDir string) string {
	return settingsoverlay.ProjectOverlayPath(policyProjectKey(projectDir), settingsoverlay.BasenameModelPolicy)
}

func policyProjectKey(projectDir string) string {
	clean := filepath.Clean(projectDir)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	// The key identifies a project, so two names for one directory must not
	// produce two policies.
	if canonical := fspath.CanonicalPath(clean); canonical != "" {
		clean = canonical
	}
	return clean
}

// Get returns the merged policy for a scope.
func (s *PolicyStore) Get(scope SettingsScope, projectDir string) (ModelPolicy, error) {
	if scope == SettingsScopeProject && projectDir != "" {
		return s.GetForProjectRoots([]string{projectDir})
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := cloneModelPolicy(s.bundled)
	if s.global != nil {
		p = mergePolicy(p, *s.global)
	}
	return p, nil
}

// GetForProjectRoots merges project policies in primary-then-active order.
func (s *PolicyStore) GetForProjectRoots(projectDirs []string) (ModelPolicy, error) {
	if err := settingsoverlay.CheckFormats(projectDirs); err != nil {
		return ModelPolicy{}, err
	}
	s.mu.RLock()
	p := cloneModelPolicy(s.bundled)
	if s.global != nil {
		p = mergePolicy(p, *s.global)
	}
	s.mu.RUnlock()
	seen := make(map[string]struct{}, len(projectDirs))
	for _, projectDir := range projectDirs {
		projectDir = strings.TrimSpace(projectDir)
		if projectDir == "" {
			continue
		}
		key := policyProjectKey(projectDir)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cached, ok, err := s.projectPolicy(key)
		if err != nil {
			return ModelPolicy{}, err
		}
		if ok {
			p = mergePolicy(p, cached)
		}
	}
	return p, nil
}

// Overlay returns one stored layer without parent merging.
func (s *PolicyStore) Overlay(scope SettingsScope, projectDir string) (ModelPolicy, error) {
	if scope == SettingsScopeProject && projectDir != "" {
		if err := settingsoverlay.CheckFormats([]string{projectDir}); err != nil {
			return ModelPolicy{}, err
		}
		cached, ok, err := s.projectPolicy(projectDir)
		if err != nil {
			return ModelPolicy{}, err
		}
		if ok {
			return cached, nil
		}
		return ModelPolicy{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.global != nil {
		return cloneModelPolicy(*s.global), nil
	}
	return ModelPolicy{}, nil
}

// projectPolicy loads the project overlay from disk into the cache.
func (s *PolicyStore) projectPolicy(projectDir string) (ModelPolicy, bool, error) {
	projectDir = policyProjectKey(projectDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	proj, err := loadPolicyOverlay(projectModelPolicyPath(projectDir))
	if err != nil {
		if os.IsNotExist(err) {
			delete(s.projectCache, projectDir)
			return ModelPolicy{}, false, nil
		}
		return ModelPolicy{}, false, err
	}
	proj = normalizePolicy(proj)
	s.projectCache[projectDir] = proj
	return proj, true, nil
}

// ModelPolicyPatch: nil fields are not written.
type ModelPolicyPatch struct {
	ThinkingOverrides *[]modelcall.ThinkingOverride
	Coordinator       *ModelRef
	Lite              *ModelRef
	AgentPool         *AgentPool
}

func (p ModelPolicyPatch) empty() bool {
	return p.Coordinator == nil && p.Lite == nil && p.AgentPool == nil && p.ThinkingOverrides == nil
}

func applyPolicyPatch(base ModelPolicy, patch ModelPolicyPatch) ModelPolicy {
	out := base
	if patch.ThinkingOverrides != nil {
		out.ThinkingOverrides = cloneThinkingOverrides(*patch.ThinkingOverrides)
	}
	if patch.Coordinator != nil {
		out.Coordinator = *patch.Coordinator
	}
	if patch.Lite != nil {
		out.Lite = *patch.Lite
	}
	if patch.AgentPool != nil {
		out.AgentPool = *patch.AgentPool
	}
	return normalizePolicy(out)
}

func policyFromPatch(patch ModelPolicyPatch) ModelPolicy {
	return applyPolicyPatch(ModelPolicy{}, patch)
}

// effectiveWithOverlay projects one prospective overlay without persisting it.
func (s *PolicyStore) effectiveWithOverlay(scope SettingsScope, overlay ModelPolicy) (ModelPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	base := s.bundled
	switch scope {
	case SettingsScopeGlobal:
		return mergePolicy(base, overlay), nil
	case SettingsScopeProject:
		if s.global != nil {
			base = mergePolicy(base, *s.global)
		}
		return mergePolicy(base, overlay), nil
	default:
		return ModelPolicy{}, fmt.Errorf("unknown settings scope %q", scope)
	}
}

// PutGlobal persists global user policy overlay fields.
func (s *PolicyStore) PutGlobal(p ModelPolicy) error {
	if err := validatePolicyShape(p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p = normalizePolicy(p)
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	if err := replacePolicyFile(s.globalPath, data); err != nil {
		return err
	}
	n := p
	s.global = &n
	return nil
}

// PutProject removes empty overlays so they inherit parent values.
func (s *PolicyStore) PutProject(projectDir string, p ModelPolicy) error {
	if err := validatePolicyShape(p); err != nil {
		return err
	}
	if err := settingsoverlay.CheckFormats([]string{projectDir}); err != nil {
		return err
	}
	projectDir = policyProjectKey(projectDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	p = normalizePolicy(p)
	path := projectModelPolicyPath(projectDir)
	if !policyHasContent(p) {
		delete(s.projectCache, projectDir)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	dir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	if err := replacePolicyFile(path, data); err != nil {
		return err
	}
	s.projectCache[projectDir] = p
	return nil
}

func replacePolicyFile(path string, data []byte) error {
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     privateConfigFileMode,
		DirMode:  privateConfigDirMode,
	})
	return err
}

// SettingsScope identifies global vs project settings.
type SettingsScope string

const (
	SettingsScopeGlobal  SettingsScope = "global"
	SettingsScopeProject SettingsScope = "project"
)
