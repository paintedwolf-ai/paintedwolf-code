package settings

import (
	"fmt"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"gopkg.in/yaml.v3"
)

// ApprovalEffect is a policy-rule outcome.
type ApprovalEffect string

const (
	ApprovalEffectDeny ApprovalEffect = "deny"
	ApprovalEffectAsk  ApprovalEffect = "ask"
)

// ApprovalCategory classifies a rule target.
type ApprovalCategory string

const (
	ApprovalCategoryTool    ApprovalCategory = "tool"
	ApprovalCategoryCommand ApprovalCategory = "command"
	ApprovalCategoryMCP     ApprovalCategory = "mcp"
	ApprovalCategoryPath    ApprovalCategory = "path"
	// ApprovalCategoryHostResource gates process host resources.
	ApprovalCategoryHostResource ApprovalCategory = "host_resource"
	// ApprovalCategoryHost gates outbound hostnames.
	ApprovalCategoryHost ApprovalCategory = "host"
	// ApprovalCategoryWriteRoot is a bounded sandbox write-root lease.
	ApprovalCategoryWriteRoot ApprovalCategory = "write_root"
	// ApprovalCategorySocketPath is a durable exact AF_UNIX local-service lease.
	ApprovalCategorySocketPath ApprovalCategory = hitl.ApprovalGrantCategorySocketPath
	// ApprovalCategorySecret grants exact secret identities.
	ApprovalCategorySecret ApprovalCategory = hitl.ApprovalGrantCategorySecret
	// ApprovalCategorySecretRedact withholds exact secret identities.
	ApprovalCategorySecretRedact ApprovalCategory = hitl.ApprovalGrantCategorySecretRedact

	// ApprovalCategoryActionSet is host-minted exact-action authority. Pattern is
	// a digest of the enumerated GrantKeys; ExactActionSet carries the keys.
	ApprovalCategoryActionSet ApprovalCategory = hitl.ApprovalGrantCategoryActionSet

	// ApprovalCategoryPackageCoordinate authorizes execution of verified package coordinates.
	ApprovalCategoryPackageCoordinate ApprovalCategory = hitl.ApprovalGrantCategoryPackageCoordinate
)

// ApprovalRule is one deny/ask policy rule.
type ApprovalRule struct {
	Category ApprovalCategory   `yaml:"category" json:"category"`
	Pattern  string             `yaml:"pattern" json:"pattern"`
	Effect   ApprovalEffect     `yaml:"effect" json:"effect"`
	Source   ApprovalRuleSource `yaml:"-" json:"-"`
}

// ApprovalRuleSource identifies a rule's policy layer and extension source.
type ApprovalRuleSource struct {
	UnitID string
	PackID string
	Scope  ApprovalRuleScope
}

// ApprovalRuleScope identifies the policy layer that supplied a rule.
type ApprovalRuleScope string

const (
	ApprovalRuleScopeDevice  ApprovalRuleScope = "device"
	ApprovalRuleScopeProject ApprovalRuleScope = "project"
)

// ApprovalRuleLayers keeps device and repository policy separate.
type ApprovalRuleLayers struct {
	Device  []ApprovalRule
	Project []ApprovalRule
}

// ApprovalGrant is one host-minted reusable approval lease.
type ApprovalGrant struct {
	ElevatedEffects []api.ElevatedAccessEffect `yaml:"elevated_effects,omitempty" json:"elevated_effects,omitempty"`
	ID              string                     `yaml:"id" json:"id"`
	Scope           hitl.ApprovalGrantScope    `yaml:"scope" json:"scope"`
	Category        ApprovalCategory           `yaml:"category" json:"category"`
	Pattern         string                     `yaml:"pattern" json:"pattern"`
	ProjectID       string                     `yaml:"project_id,omitempty" json:"project_id,omitempty"`
	ProjectDir      string                     `yaml:"project_dir,omitempty" json:"project_dir,omitempty"`
	Title           string                     `yaml:"title" json:"title"`
	Coverage        string                     `yaml:"coverage" json:"coverage"`
	GrantedAt       time.Time                  `yaml:"granted_at" json:"granted_at"`
	ExpiresAt       *time.Time                 `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	ExpiresWhen     string                     `yaml:"expires_when" json:"expires_when"`
	ReaskWhen       string                     `yaml:"reask_when" json:"reask_when"`
	Witness         hitl.ApprovalGrantWitness  `yaml:"witness" json:"witness"`
	// GrantedPath stores durable filesystem access.
	GrantedPath *hitl.GrantedPathDelta `yaml:"granted_path,omitempty" json:"granted_path,omitempty"`
	// ApprovedPath and ResolvedPath are first-class socket_path fields.
	ApprovedPath     string `yaml:"approved_path,omitempty" json:"approved_path,omitempty"`
	ResolvedPath     string `yaml:"resolved_path,omitempty" json:"resolved_path,omitempty"`
	Source           string `yaml:"source,omitempty" json:"source,omitempty"`
	OwnerOperationID string `yaml:"owner_operation_id,omitempty" json:"-"`
	// GrantedByPersonID and GrantedByPolicy name whoever approved the grant;
	// exactly one is set.
	GrantedByPersonID string                      `yaml:"granted_by_person_id,omitempty" json:"granted_by_person_id,omitempty"`
	GrantedByPolicy   *authzledger.PolicyIdentity `yaml:"granted_by_policy,omitempty" json:"granted_by_policy,omitempty"`
	// SecretFingerprints compose coverage across active grants.
	SecretFingerprints []string `yaml:"secret_fingerprints,omitempty" json:"secret_fingerprints,omitempty"`
	// SecretDestinationID binds configured provider trust.
	SecretDestinationID string                  `yaml:"secret_destination_id,omitempty" json:"secret_destination_id,omitempty"`
	SecretRecipients    []secretmatch.Recipient `yaml:"secret_recipients,omitempty" json:"secret_recipients,omitempty"`
	SecretNames         []string                `yaml:"secret_names,omitempty" json:"secret_names,omitempty"`
	// ExactActionSet contains the covered GrantKeys.
	ExactActionSet []string `yaml:"exact_action_set,omitempty" json:"exact_action_set,omitempty"`
}

// ApprovalConfig is the on-disk approvals.yaml shape.
type ApprovalConfig struct {
	Rules  []ApprovalRule  `yaml:"rules" json:"rules"`
	Grants []ApprovalGrant `yaml:"grants,omitempty" json:"grants,omitempty"`
	// internal/gate maps posture to active approval gates.
	Posture gate.Posture `yaml:"approval_posture,omitempty" json:"approval_posture,omitempty"`
	// AIRationale toggles generated approval-card rationale.
	AIRationale *bool `yaml:"ai_rationale,omitempty" json:"ai_rationale,omitempty"`
	// NeverAsk disables discretionary approval prompts.
	NeverAsk *bool `yaml:"never_ask,omitempty" json:"never_ask,omitempty"`
}

// ApprovalStore loads bundled, global, and project approval overlays.
type ApprovalStore struct {
	// writeMu serializes global read-modify-write cycles.
	writeMu      sync.Mutex
	mu           sync.RWMutex
	globalPath   string
	bundled      ApprovalConfig
	global       *ApprovalConfig
	projectCache map[string]ApprovalConfig
}

// NewApprovalStoreAt loads the global overlay at globalPath.
func NewApprovalStoreAt(globalPath string) (*ApprovalStore, error) {
	s := &ApprovalStore{
		globalPath:   globalPath,
		projectCache: make(map[string]ApprovalConfig),
	}
	if err := s.reloadBundled(); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(globalPath); err == nil {
		var cfg ApprovalConfig
		if err := config.DecodeYAML(data, &cfg); err != nil {
			return nil, approvalFileRecoveryError(globalPath, err)
		}
		cfg = normalizeApprovalConfig(cfg)
		if err := validateApprovalConfig(cfg); err != nil {
			return nil, approvalFileRecoveryError(globalPath, err)
		}
		cfg.Grants = pruneExpiredGrants(cfg.Grants, time.Now().UTC())
		s.global = &cfg
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func approvalFileRecoveryError(path string, cause error) error {
	return fmt.Errorf("cannot load approvals %q: %w; the file is unchanged. Make a private copy, correct the reported entry, and restart. For an incompatible secret lease, remove only that grant and approve its next request. See docs/compatibility.md#device-configuration", path, cause)
}

// NewApprovalStore loads bundled defaults and optional global user approvals.
func NewApprovalStore() (*ApprovalStore, error) {
	globalPath, err := userApprovalsPath()
	if err != nil {
		return nil, err
	}
	return NewApprovalStoreAt(globalPath)
}

func loadApprovalFile(path string) (ApprovalConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ApprovalConfig{}, err
	}
	var cfg ApprovalConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return ApprovalConfig{}, fmt.Errorf("parse approvals %s: %w", path, err)
	}
	return normalizeApprovalConfig(cfg), nil
}

// loadProjectApprovalFile reads and tightens repository policy.
func loadProjectApprovalFile(path string) (ApprovalConfig, error) {
	cfg, err := loadApprovalFile(path)
	if err != nil {
		return ApprovalConfig{}, err
	}
	return clampProjectApprovalConfig(cfg), nil
}

// clampProjectApprovalConfig removes authority from repository policy.
func clampProjectApprovalConfig(cfg ApprovalConfig) ApprovalConfig {
	rules := make([]ApprovalRule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if allowedEffects[rule.Effect] {
			rules = append(rules, rule)
		}
	}
	cfg.Rules = rules
	cfg.Grants = nil
	// A repository contributes a valid posture or none: gate.Stricter ranks an
	// unknown token at the default, so it would otherwise outrank a Light device.
	if !gate.ValidPosture(string(cfg.Posture)) {
		cfg.Posture = ""
	}
	return cfg
}

func normalizeApprovalConfig(cfg ApprovalConfig) ApprovalConfig {
	out := make([]ApprovalRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		r.Category = ApprovalCategory(strings.TrimSpace(string(r.Category)))
		r.Pattern = strings.TrimSpace(r.Pattern)
		r.Effect = ApprovalEffect(strings.TrimSpace(string(r.Effect)))
		if r.Category == "" || r.Pattern == "" || r.Effect == "" {
			continue
		}
		out = append(out, r)
	}
	cfg.Rules = out
	grants := make([]ApprovalGrant, 0, len(cfg.Grants))
	for _, grant := range cfg.Grants {
		grant.ID = strings.TrimSpace(grant.ID)
		grant.Category = ApprovalCategory(strings.TrimSpace(string(grant.Category)))
		grant.Pattern = strings.TrimSpace(grant.Pattern)
		grant.ProjectID = strings.TrimSpace(grant.ProjectID)
		grant.ProjectDir = strings.TrimSpace(grant.ProjectDir)
		grant.OwnerOperationID = strings.TrimSpace(grant.OwnerOperationID)
		grant.GrantedByPersonID = strings.TrimSpace(grant.GrantedByPersonID)
		if grant.ID == "" || grant.Category == "" || grant.Pattern == "" || grant.Scope == "" {
			continue
		}
		grants = append(grants, grant)
	}
	cfg.Grants = grants
	if cfg.Posture != "" {
		cfg.Posture = gate.PostureFromString(string(cfg.Posture))
	}
	return cfg
}

// Grants expire on both load and write.
func pruneExpiredGrants(grants []ApprovalGrant, now time.Time) []ApprovalGrant {
	out := make([]ApprovalGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			continue
		}
		out = append(out, grant)
	}
	return out
}

func ruleKey(r ApprovalRule) string {
	return string(r.Category) + "\x00" + r.Pattern
}

func mergeApprovalRules(base, overlay []ApprovalRule) []ApprovalRule {
	return mergeRulesWith(base, overlay, func(_, incoming ApprovalRule) ApprovalRule {
		return incoming
	})
}

// mergeProjectApprovalRules preserves the stricter effect on key collisions.
func mergeProjectApprovalRules(base, projectRules []ApprovalRule) []ApprovalRule {
	return mergeRulesWith(base, projectRules, func(existing, incoming ApprovalRule) ApprovalRule {
		if approvalEffectRank(incoming.Effect) > approvalEffectRank(existing.Effect) {
			return incoming
		}
		return existing
	})
}

// approvalEffectRank orders policy effects from less to more restrictive.
func approvalEffectRank(e ApprovalEffect) int {
	switch e {
	case ApprovalEffectDeny:
		return 2
	case ApprovalEffectAsk:
		return 1
	default:
		return 0
	}
}

// mergeRulesWith merges overlay onto base in base-first key order, calling resolve for
// each key both layers set.
func mergeRulesWith(base, overlay []ApprovalRule, resolve func(existing, incoming ApprovalRule) ApprovalRule) []ApprovalRule {
	byKey := make(map[string]ApprovalRule, len(base)+len(overlay))
	order := make([]string, 0, len(base)+len(overlay))
	for _, r := range base {
		k := ruleKey(r)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = r
	}
	for _, r := range overlay {
		k := ruleKey(r)
		existing, ok := byKey[k]
		if !ok {
			order = append(order, k)
			byKey[k] = r
			continue
		}
		byKey[k] = resolve(existing, r)
	}
	out := make([]ApprovalRule, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

func (s *ApprovalStore) reloadBundled() error {
	cfg, err := bundledApprovalConfig()
	if err != nil {
		return err
	}
	if err := validateApprovalConfig(cfg); err != nil {
		return fmt.Errorf("validate bundled approvals: %w", err)
	}
	s.bundled = cfg
	return nil
}

// ProjectRef carries stable identity and the active overlay directory.
type ProjectRef struct {
	// ID is the canonical project identity. Empty means global scope.
	ID string
	// Dir is the active folder the committed <overlay>/approvals.yaml is read from.
	Dir string
}

// Get returns effective approval policy for a scope.
func (s *ApprovalStore) Get(scope llm.SettingsScope, ref ProjectRef) ApprovalConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rules := append([]ApprovalRule(nil), s.bundled.Rules...)
	if s.global != nil {
		rules = mergeApprovalRules(rules, s.global.Rules)
	}
	posture := s.postureLocked()
	enabled := s.aiRationaleEnabledLocked()
	neverAsk := s.neverAskLocked()
	if scope == llm.SettingsScopeProject && ref.Dir != "" {
		proj := s.projectOverlayLocked(ref.Dir)
		rules = mergeProjectApprovalRules(rules, proj.Rules)
		// Repository posture can only tighten device posture.
		posture = gate.Stricter(posture, proj.Posture)
		// Repository policy may suppress generated rationale.
		if proj.AIRationale != nil {
			enabled = enabled && *proj.AIRationale
		}
		// Repository policy may restore approval prompts.
		if proj.NeverAsk != nil {
			neverAsk = neverAsk && *proj.NeverAsk
		}
	}
	return ApprovalConfig{
		Rules:       rules,
		Grants:      s.grantsLocked(ref.ID),
		Posture:     posture,
		AIRationale: &enabled,
		NeverAsk:    &neverAsk,
	}
}

// RuleLayers keeps device and repository policy in separate layers.
func (s *ApprovalStore) RuleLayers(scope llm.SettingsScope, ref ProjectRef) ApprovalRuleLayers {
	s.mu.RLock()
	defer s.mu.RUnlock()
	device := append([]ApprovalRule(nil), s.bundled.Rules...)
	if s.global != nil {
		device = mergeApprovalRules(device, s.global.Rules)
	}
	for i := range device {
		device[i].Source.Scope = ApprovalRuleScopeDevice
	}
	var projectRules []ApprovalRule
	if scope == llm.SettingsScopeProject && ref.Dir != "" {
		projectRules = append(projectRules, s.projectOverlayLocked(ref.Dir).Rules...)
		for i := range projectRules {
			projectRules[i].Source.Scope = ApprovalRuleScopeProject
		}
	}
	return ApprovalRuleLayers{Device: device, Project: projectRules}
}

// grantsLocked returns active grants for a project.
func (s *ApprovalStore) grantsLocked(projectID string) []ApprovalGrant {
	if s.global == nil {
		return nil
	}
	now := time.Now().UTC()
	out := make([]ApprovalGrant, 0, len(s.global.Grants))
	for _, grant := range s.global.Grants {
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			continue
		}
		if grant.Scope == hitl.ApprovalGrantScopeProject && grant.ProjectID != projectID {
			continue
		}
		out = append(out, grant)
	}
	return out
}

// aiRationaleEnabledLocked returns the device rationale setting.
func (s *ApprovalStore) aiRationaleEnabledLocked() bool {
	if s.global != nil && s.global.AIRationale != nil {
		return *s.global.AIRationale
	}
	if s.bundled.AIRationale != nil {
		return *s.bundled.AIRationale
	}
	return true
}

// AIRationaleEnabled returns whether approval cards may show a host-generated AI rationale.
func (s *ApprovalStore) AIRationaleEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.aiRationaleEnabledLocked()
}

// neverAskLocked reads device policy; project policy may only restore asks.
func (s *ApprovalStore) neverAskLocked() bool {
	if s.global != nil && s.global.NeverAsk != nil {
		return *s.global.NeverAsk
	}
	if s.bundled.NeverAsk != nil {
		return *s.bundled.NeverAsk
	}
	return false
}

// NeverAsk disables discretionary prompts; hard boundaries still apply.
func (s *ApprovalStore) NeverAsk() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.neverAskLocked()
}

// postureLocked resolves the effective global posture (caller holds the lock).
func (s *ApprovalStore) postureLocked() gate.Posture {
	if s.global != nil && s.global.Posture != "" {
		return s.global.Posture
	}
	if s.bundled.Posture != "" {
		return s.bundled.Posture
	}
	return gate.DefaultPosture
}

// Posture returns the effective global posture.
func (s *ApprovalStore) Posture() gate.Posture {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.postureLocked()
}

// EgressPosture derives the network default from the active gate table.
func (s *ApprovalStore) EgressPosture() confine.EgressPosture {
	return EgressPostureFor(s.Posture())
}

// EgressPostureFor maps an ask-line to the egress default it implies.
func EgressPostureFor(p gate.Posture) confine.EgressPosture {
	if p.AsksOnFirstHost() {
		return confine.PostureAsk
	}
	return confine.PostureObserve
}

// Unscoped egress reads bundled and device rules together.
func (s *ApprovalStore) GlobalRules() []ApprovalRule {
	return s.Get(llm.SettingsScopeGlobal, ProjectRef{}).Rules
}

// MergedFrom reports which layers contributed to the effective config.
func (s *ApprovalStore) MergedFrom(scope llm.SettingsScope, projectDir string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	layers := []string{"bundled"}
	if s.global != nil && (len(s.global.Rules) > 0 || len(s.global.Grants) > 0 || s.global.Posture != "" ||
		s.global.AIRationale != nil || s.global.NeverAsk != nil) {
		layers = append(layers, "global")
	}
	if scope == llm.SettingsScopeProject && projectDir != "" {
		proj := s.projectOverlayLocked(projectDir)
		if projectOverlayActive(proj) {
			layers = append(layers, "project")
		}
	}
	return layers
}

func projectOverlayActive(cfg ApprovalConfig) bool {
	return len(cfg.Rules) > 0 || cfg.Posture != "" || cfg.AIRationale != nil || cfg.NeverAsk != nil
}

// projectOverlayLocked returns the project overlay (cache or disk). Caller holds the lock.
func (s *ApprovalStore) projectOverlayLocked(projectDir string) ApprovalConfig {
	if projectDir == "" {
		return ApprovalConfig{}
	}
	if cached, ok := s.projectCache[projectDir]; ok {
		return cached
	}
	// Read-locked callers load from disk without mutating the cache.
	proj, err := loadProjectApprovalFile(projectApprovalsPath(projectDir))
	if err != nil {
		return ApprovalConfig{}
	}
	return proj
}

// UpsertGlobalGrant stores a host-minted project or device lease.
func (s *ApprovalStore) UpsertGlobalGrant(grant ApprovalGrant) (bool, error) {
	if grant.Scope != hitl.ApprovalGrantScopeProject && grant.Scope != hitl.ApprovalGrantScopeDevice {
		return false, fmt.Errorf("only project and device grants are durable")
	}
	if grant.ID == "" || grant.Category == "" || strings.TrimSpace(grant.Pattern) == "" {
		return false, fmt.Errorf("grant id, category, and pattern are required")
	}
	if grant.Category == ApprovalCategorySocketPath {
		if strings.TrimSpace(grant.ApprovedPath) == "" || strings.TrimSpace(grant.ResolvedPath) == "" {
			return false, fmt.Errorf("socket_path grant requires approved_path and resolved_path")
		}
	}
	if err := grant.ToDomain().ValidateDurableIdentity(); err != nil {
		return false, err
	}
	if !grant.ToDomain().Granted() {
		return false, fmt.Errorf("durable grant requires the person or policy that approved it")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.RLock()
	var rules []ApprovalRule
	var grants []ApprovalGrant
	posture := s.postureLocked()
	ai := s.aiRationaleEnabledLocked()
	neverAsk := s.neverAskLocked()
	if s.global != nil {
		rules = append([]ApprovalRule(nil), s.global.Rules...)
		grants = append([]ApprovalGrant(nil), s.global.Grants...)
	}
	s.mu.RUnlock()
	created := true
	next := make([]ApprovalGrant, 0, len(grants)+1)
	for _, existing := range grants {
		if existing.ID == grant.ID {
			if existing.ExpiresAt == nil || existing.ExpiresAt.After(time.Now()) {
				return false, nil
			}
			continue
		}
		next = append(next, existing)
	}
	if grant.GrantedAt.IsZero() {
		grant.GrantedAt = time.Now().UTC()
	}
	next = append(next, grant)
	err := s.putGlobalLocked(ApprovalConfig{
		Rules:       rules,
		Grants:      next,
		Posture:     posture,
		AIRationale: &ai,
		NeverAsk:    &neverAsk,
	})
	return created, err
}

// GlobalGrants returns active durable grants from the user overlay.
func (s *ApprovalStore) GlobalGrants() []ApprovalGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.global == nil {
		return nil
	}
	now := time.Now().UTC()
	out := make([]ApprovalGrant, 0, len(s.global.Grants))
	for _, grant := range s.global.Grants {
		if grant.ExpiresAt == nil || grant.ExpiresAt.After(now) {
			out = append(out, cloneSecretPermissionGrant(grant))
		}
	}
	return out
}

// WriteRootsForProject returns active write-root grants for a project.
func (s *ApprovalStore) WriteRootsForProject(projectID string) []string {
	grants := s.GlobalGrants()
	out := make([]string, 0, len(grants))
	for _, grant := range grants {
		if grant.Category != ApprovalCategoryWriteRoot {
			continue
		}
		if grant.Scope == hitl.ApprovalGrantScopeDevice {
			out = append(out, grant.Pattern)
			continue
		}
		if grant.ProjectID != projectID {
			continue
		}
		out = append(out, grant.Pattern)
	}
	return out
}

// SocketPathsForProject returns active, revalidated socket grants.
func (s *ApprovalStore) SocketPathsForProject(projectID string) []confine.SocketGrant {
	grants := s.GlobalGrants()
	out := make([]confine.SocketGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.Category != ApprovalCategorySocketPath {
			continue
		}
		if grant.Scope == hitl.ApprovalGrantScopeProject {
			if grant.ProjectID != projectID {
				continue
			}
		} else if grant.Scope != hitl.ApprovalGrantScopeDevice {
			continue
		}
		g := confine.SocketGrant{
			ApprovedPath: strings.TrimSpace(grant.ApprovedPath),
			ResolvedPath: strings.TrimSpace(grant.ResolvedPath),
		}
		if g.ApprovedPath == "" || g.ResolvedPath == "" {
			continue
		}
		if err := confine.RevalidateSocketGrant(g); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out
}

// RevokeGlobalGrant removes one durable grant by ID.
func (s *ApprovalStore) RevokeGlobalGrant(id string) (bool, error) {
	return s.revokeGlobalGrant(id, "")
}

// RevokeGlobalGrantInstalledBy removes a durable grant only while operationID identifies its installer.
func (s *ApprovalStore) RevokeGlobalGrantInstalledBy(id, operationID string) (bool, error) {
	if strings.TrimSpace(operationID) == "" {
		return false, fmt.Errorf("invalid approval operation id")
	}
	return s.revokeGlobalGrant(id, operationID)
}

func (s *ApprovalStore) revokeGlobalGrant(id, operationID string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, fmt.Errorf("invalid grant id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.RLock()
	var rules []ApprovalRule
	var grants []ApprovalGrant
	posture := s.postureLocked()
	ai := s.aiRationaleEnabledLocked()
	neverAsk := s.neverAskLocked()
	if s.global != nil {
		rules = append([]ApprovalRule(nil), s.global.Rules...)
		grants = append([]ApprovalGrant(nil), s.global.Grants...)
	}
	s.mu.RUnlock()
	next := make([]ApprovalGrant, 0, len(grants))
	found := false
	for _, grant := range grants {
		if grant.ID == id && (operationID == "" || grant.OwnerOperationID == operationID) {
			found = true
			continue
		}
		next = append(next, grant)
	}
	if !found {
		return false, nil
	}
	return true, s.putGlobalLocked(ApprovalConfig{
		Rules:       rules,
		Grants:      next,
		Posture:     posture,
		AIRationale: &ai,
		NeverAsk:    &neverAsk,
	})
}

// PutGlobal persists the global user approval overlay (posture, policy, and grants).
func (s *ApprovalStore) PutGlobal(cfg ApprovalConfig) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.putGlobalLocked(cfg)
}

// putGlobalLocked is PutGlobal for callers already holding writeMu.
func (s *ApprovalStore) putGlobalLocked(cfg ApprovalConfig) error {
	cfg = normalizeApprovalConfig(cfg)
	if err := validateApprovalConfig(cfg); err != nil {
		return err
	}
	cfg.Grants = pruneExpiredGrants(cfg.Grants, time.Now().UTC())
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(s.globalPath, data); err != nil {
		return err
	}
	s.mu.Lock()
	n := cloneApprovalConfig(cfg)
	s.global = &n
	s.mu.Unlock()
	return nil
}

// OverlayRules returns a copy of the global user overlay rules (not merged).
func (s *ApprovalStore) OverlayRules() []ApprovalRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.global == nil {
		return nil
	}
	return append([]ApprovalRule(nil), s.global.Rules...)
}

// ProjectOverlay returns a copy of the project overlay config (not merged with global).
func (s *ApprovalStore) ProjectOverlay(projectDir string) ApprovalConfig {
	if projectDir == "" {
		return ApprovalConfig{}
	}
	s.mu.RLock()
	if cached, ok := s.projectCache[projectDir]; ok {
		out := cached
		s.mu.RUnlock()
		return cloneApprovalConfig(out)
	}
	s.mu.RUnlock()
	proj, err := loadProjectApprovalFile(projectApprovalsPath(projectDir))
	if err != nil {
		return ApprovalConfig{}
	}
	return proj
}

// ProjectOverlayRules returns a copy of the project overlay rules.
func (s *ApprovalStore) ProjectOverlayRules(projectDir string) []ApprovalRule {
	return append([]ApprovalRule(nil), s.ProjectOverlay(projectDir).Rules...)
}

// PutProject persists a project overlay at <overlay>/approvals.yaml. Empty posture and
// nil toggles mean inherit the global values at Get time.
func (s *ApprovalStore) PutProject(projectDir string, cfg ApprovalConfig) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.putProjectLocked(projectDir, cfg)
}

func (s *ApprovalStore) putProjectLocked(projectDir string, cfg ApprovalConfig) error {
	cfg = normalizeApprovalConfig(cfg)
	if len(cfg.Grants) > 0 {
		return fmt.Errorf("project approval policy cannot contain grants")
	}
	if err := validateApprovalConfig(cfg); err != nil {
		return err
	}
	path := projectApprovalsPath(projectDir)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeSettingsFile(path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.projectCache[projectDir] = cfg
	s.mu.Unlock()
	return nil
}

// SetHostResourceRule updates one resource rule. Removing it exposes any broader matching rule.
func (s *ApprovalStore) SetHostResourceRule(scope llm.SettingsScope, projectDir, resourceID string, effect ApprovalEffect) error {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return fmt.Errorf("host resource id is required")
	}
	if effect != "" && !allowedEffects[effect] {
		return fmt.Errorf("invalid host-resource effect %q", effect)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	switch scope {
	case llm.SettingsScopeGlobal:
		s.mu.RLock()
		cfg := ApprovalConfig{}
		if s.global != nil {
			cfg = cloneApprovalConfig(*s.global)
		}
		s.mu.RUnlock()
		cfg.Rules = replaceHostResourceRule(cfg.Rules, resourceID, effect)
		return s.putGlobalLocked(cfg)
	case llm.SettingsScopeProject:
		if strings.TrimSpace(projectDir) == "" {
			return fmt.Errorf("project directory is required")
		}
		cfg := s.ProjectOverlay(projectDir)
		cfg.Rules = replaceHostResourceRule(cfg.Rules, resourceID, effect)
		return s.putProjectLocked(projectDir, cfg)
	default:
		return fmt.Errorf("invalid settings scope %q", scope)
	}
}

func replaceHostResourceRule(rules []ApprovalRule, resourceID string, effect ApprovalEffect) []ApprovalRule {
	out := make([]ApprovalRule, 0, len(rules)+1)
	for _, rule := range rules {
		if rule.Category == ApprovalCategoryHostResource && rule.Pattern == resourceID {
			continue
		}
		out = append(out, rule)
	}
	if effect != "" {
		out = append(out, ApprovalRule{Category: ApprovalCategoryHostResource, Pattern: resourceID, Effect: effect})
	}
	return out
}

func cloneApprovalConfig(cfg ApprovalConfig) ApprovalConfig {
	out := cfg
	if cfg.Rules != nil {
		out.Rules = append([]ApprovalRule(nil), cfg.Rules...)
	}
	if cfg.Grants != nil {
		out.Grants = append([]ApprovalGrant(nil), cfg.Grants...)
		for i := range out.Grants {
			out.Grants[i] = cloneSecretPermissionGrant(out.Grants[i])
		}
	}
	if cfg.AIRationale != nil {
		v := *cfg.AIRationale
		out.AIRationale = &v
	}
	return out
}

var allowedPolicyCategories = map[ApprovalCategory]bool{
	ApprovalCategoryTool:         true,
	ApprovalCategoryCommand:      true,
	ApprovalCategoryMCP:          true,
	ApprovalCategoryPath:         true,
	ApprovalCategoryHost:         true,
	ApprovalCategoryWriteRoot:    true,
	ApprovalCategoryHostResource: true,
}

var allowedDurableGrantCategories = map[ApprovalCategory]bool{
	ApprovalCategoryTool:              true,
	ApprovalCategoryMCP:               true,
	ApprovalCategoryPath:              true,
	ApprovalCategoryHost:              true,
	ApprovalCategoryWriteRoot:         true,
	ApprovalCategorySocketPath:        true,
	ApprovalCategoryHostResource:      true,
	ApprovalCategorySecret:            true,
	ApprovalCategorySecretRedact:      true,
	ApprovalCategoryActionSet:         true,
	ApprovalCategoryPackageCoordinate: true,
}

var allowedEffects = map[ApprovalEffect]bool{
	ApprovalEffectDeny: true,
	ApprovalEffectAsk:  true,
}

// ValidatePolicyRules validates client-authored deny/ask policy without treating
// malformed authority as an internal persistence failure.
func ValidatePolicyRules(rules []ApprovalRule) error {
	return validateApprovalConfig(ApprovalConfig{Rules: rules})
}

func validateApprovalConfig(cfg ApprovalConfig) error {
	if cfg.Posture != "" && gate.PostureFromString(string(cfg.Posture)) != cfg.Posture {
		return fmt.Errorf("invalid approval posture %q", cfg.Posture)
	}
	for _, r := range cfg.Rules {
		if !allowedPolicyCategories[r.Category] {
			return fmt.Errorf("invalid approval category %q", r.Category)
		}
		if !allowedEffects[r.Effect] {
			return fmt.Errorf("invalid approval effect %q", r.Effect)
		}
		if r.Pattern == "" {
			return fmt.Errorf("approval pattern is required")
		}
		if r.Category == ApprovalCategoryWriteRoot {
			if !filepath.IsAbs(r.Pattern) {
				return fmt.Errorf("write_root pattern must be an absolute directory")
			}
		}
	}
	for _, grant := range cfg.Grants {
		if !allowedDurableGrantCategories[grant.Category] {
			return fmt.Errorf("invalid grant category %q", grant.Category)
		}
		if grant.Scope != hitl.ApprovalGrantScopeProject && grant.Scope != hitl.ApprovalGrantScopeDevice {
			return fmt.Errorf("invalid durable grant scope %q", grant.Scope)
		}
		if grant.ID == "" || grant.Pattern == "" || grant.Title == "" {
			return fmt.Errorf("grant id, pattern, and title are required")
		}
		if err := grant.ToDomain().ValidateDurableIdentity(); err != nil {
			return err
		}
		if grant.Category == ApprovalCategoryPackageCoordinate {
			if grant.ExpiresAt == nil {
				return fmt.Errorf("package_coordinate grant must be an expiring lease")
			}
		}
		if grant.Category == ApprovalCategorySocketPath {
			if strings.TrimSpace(grant.ApprovedPath) == "" || strings.TrimSpace(grant.ResolvedPath) == "" {
				return fmt.Errorf("socket_path grant requires approved_path and resolved_path")
			}
		}
		if grant.Category == ApprovalCategorySecretRedact {
			if grant.ExpiresAt == nil || strings.TrimSpace(grant.ProjectID) == "" {
				return fmt.Errorf("secret_redact grant must be an expiring rule bound to a canonical project")
			}
			if grant.Scope != hitl.ApprovalGrantScopeProject && grant.Scope != hitl.ApprovalGrantScopeDevice {
				return fmt.Errorf("secret_redact grant must be project or device scoped")
			}
		}
		if grant.Category == ApprovalCategorySecret {
			if grant.Scope != hitl.ApprovalGrantScopeProject || grant.ExpiresAt == nil || strings.TrimSpace(grant.ProjectID) == "" {
				return fmt.Errorf("secret grant must be an expiring lease bound to a canonical project")
			}
			if err := hitl.ValidateSecretRelease(grant.ToDomain()); err != nil {
				return fmt.Errorf("secret grant %q: %w", grant.ID, err)
			}
			fingerprints := make([]secretmatch.SecretFingerprint, 0, len(grant.SecretFingerprints))
			for _, fingerprint := range grant.SecretFingerprints {
				if fingerprint == "" || strings.TrimSpace(fingerprint) != fingerprint {
					return fmt.Errorf("secret grant fingerprints must be canonical non-empty identities")
				}
				fingerprints = append(fingerprints, secretmatch.SecretFingerprint(fingerprint))
			}
			if len(fingerprints) == 0 || grant.Pattern != secretmatch.FingerprintDigest(fingerprints) {
				return fmt.Errorf("secret grant requires an exact fingerprint set matching its pattern")
			}
		}
		if grant.Category == ApprovalCategoryActionSet {
			if len(grant.ExactActionSet) == 0 {
				return fmt.Errorf("action_set grant requires exact_action_set")
			}
			for _, key := range grant.ExactActionSet {
				if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key {
					return fmt.Errorf("action_set grant keys must be canonical non-empty identities")
				}
			}
		}
	}
	return nil
}

// bundledApprovalConfig reads the embedded defaults.
func bundledApprovalConfig() (ApprovalConfig, error) {
	data, err := config.Read(config.SecurityApprovals)
	if err != nil {
		return ApprovalConfig{}, err
	}
	var cfg ApprovalConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return ApprovalConfig{}, fmt.Errorf("parse bundled config: %w", err)
	}
	return normalizeApprovalConfig(cfg), nil
}

func cloneSecretPermissionGrant(grant ApprovalGrant) ApprovalGrant {
	grant.ElevatedEffects = append(grant.ElevatedEffects[:0:0], grant.ElevatedEffects...)
	grant.SecretFingerprints = append([]string(nil), grant.SecretFingerprints...)
	grant.SecretNames = append([]string(nil), grant.SecretNames...)
	grant.SecretRecipients = append([]secretmatch.Recipient(nil), grant.SecretRecipients...)
	return grant
}
