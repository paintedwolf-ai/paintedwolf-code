// Package catalogview builds registries from an effective extension catalog.
package catalogview

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/approvalregistry"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/mcp/bindings"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// View holds registries derived from one EffectiveCatalog.
type View struct {
	Catalog         *extpacks.EffectiveCatalog
	Policy          []hintregistry.Entry
	Rules           *oar.RuleSet
	Anchors         *anchor.Registry
	ToolProfiles    []sandbox.ToolProfile
	AgentProfiles   []agentdef.Profile
	Approvals       []approvalregistry.Entry
	ApprovalRules   []extpacks.ApprovalRuleUnit
	Playbooks       *prompts.PlaybookMatcher
	ToolSchemas     *toolschema.Config
	Bindings        []bindings.Binding
	Contributions   *contribution.Set
	CredentialSlots *secretmint.Inspector
	// VocabularyNotes name a rule whose remedy another author's profile cannot
	// reach. The authoring gate prints them; nothing here fails for one.
	VocabularyNotes []string

	// Detection packs compile only when requested.
	detectionOnce  sync.Once
	detectionPacks []detectionpack.Pack
}

// DetectionPacks returns contributed packs in catalog order.
func (v *View) DetectionPacks() []detectionpack.Pack {
	if v == nil {
		return nil
	}
	v.detectionOnce.Do(func() {
		// Resolve has already recorded loader diagnostics.
		v.detectionPacks, _ = extpacks.LoadEffectiveDetectionPacks(v.Catalog)
	})
	return v.detectionPacks
}

// WithRules returns the same catalog view with another rule set.
func (v *View) WithRules(rules *oar.RuleSet) *View {
	if v == nil || v.Rules == rules {
		return v
	}
	return &View{
		Catalog:         v.Catalog,
		Policy:          v.Policy,
		Rules:           rules,
		Anchors:         v.Anchors,
		ToolProfiles:    v.ToolProfiles,
		AgentProfiles:   v.AgentProfiles,
		Approvals:       v.Approvals,
		ApprovalRules:   v.ApprovalRules,
		Playbooks:       v.Playbooks,
		ToolSchemas:     v.ToolSchemas,
		Bindings:        v.Bindings,
		Contributions:   v.Contributions,
		CredentialSlots: v.CredentialSlots,
		VocabularyNotes: v.VocabularyNotes,
	}
}

// Get resolves an agent profile from this catalog view.
func (v *View) Get(id string) (agentdef.Profile, error) {
	if v != nil {
		for _, profile := range v.AgentProfiles {
			if profile.ID == id {
				return profile, nil
			}
		}
	}
	return agentdef.Profile{}, fmt.Errorf("agent profile %q not found in catalog view", id)
}

// Build constructs one complete catalog view.
func Build(ctx context.Context, moduleRoot string, eff *extpacks.EffectiveCatalog) (*View, error) {
	_ = ctx
	if eff == nil {
		return nil, fmt.Errorf("catalogview: nil effective catalog")
	}
	moduleRoot = strings.TrimSpace(moduleRoot)
	if moduleRoot == "" {
		return nil, fmt.Errorf("catalogview: empty module root")
	}

	policy, err := hintregistry.ListEffectiveWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview policy: %w", err)
	}

	// Bundled registries do not depend on a checkout config tree.
	if err := anchorcatalog.InstallBundled(); err != nil {
		return nil, fmt.Errorf("catalogview anchor catalog: %w", err)
	}
	schemaDir := configlayout.SchemasDir(moduleRoot)
	if err := oar.InstallCapabilityBundled(schemaDir); err != nil {
		return nil, fmt.Errorf("catalogview oar capability: %w", err)
	}
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("catalogview oar loader: %w", err)
	}
	loader.SetDetectors(oar.HostDetectors(nil))
	rules, err := loader.LoadEffectivePolicyEntries(policy)
	if err != nil {
		return nil, fmt.Errorf("catalogview rules: %w", err)
	}

	if err := oar.ValidateHostRuleSet(rules); err != nil {
		return nil, fmt.Errorf("catalogview rules: %w", err)
	}

	anchors, err := anchor.LoadRegistryFromConfigRootWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview anchors: %w", err)
	}

	toolProfiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview tool profiles: %w", err)
	}

	agentProfiles, err := agentdef.LoadEffectiveWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview agent profiles: %w", err)
	}

	approvals, err := approvalregistry.ListEffectiveWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview approvals: %w", err)
	}
	approvalRules, _, err := extpacks.LoadEffectiveApprovalRules(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview approval rules: %w", err)
	}

	personaContract, err := prompts.LoadPersonaContractWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview persona contract: %w", err)
	}
	if err := validatePersonaCoverage(personaContract, agentProfiles); err != nil {
		return nil, fmt.Errorf("catalogview persona contract: %w", err)
	}
	vocabNotes, err := checkToolVocabulary(eff, toolProfiles, agentProfiles, rules)
	if err != nil {
		return nil, fmt.Errorf("catalogview tool vocabulary: %w", err)
	}
	playbooks, err := prompts.LoadPlaybookMatcherEffectiveWithCatalog(eff, personaContract)
	if err != nil {
		return nil, fmt.Errorf("catalogview playbooks: %w", err)
	}

	// Resolve has already recorded loader diagnostics.
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview tool schemas: %w", err)
	}

	binds, _, err := extpacks.LoadEffectiveBindings(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview bindings: %w", err)
	}

	contributions, err := compileContributions(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview contributions: %w", err)
	}
	credentialSlots, err := compileCredentialSlots(eff)
	if err != nil {
		return nil, fmt.Errorf("catalogview credential slots: %w", err)
	}

	return &View{
		Catalog:         eff,
		Policy:          policy,
		Rules:           rules,
		Anchors:         anchors,
		ToolProfiles:    toolProfiles,
		AgentProfiles:   agentProfiles,
		Approvals:       approvals,
		ApprovalRules:   approvalRules,
		Playbooks:       playbooks,
		ToolSchemas:     schemas,
		Bindings:        binds,
		Contributions:   contributions,
		CredentialSlots: credentialSlots,
		VocabularyNotes: vocabNotes,
	}, nil
}

// compileContributions compiles the winning immutable units.
func compileContributions(eff *extpacks.EffectiveCatalog) (*contribution.Set, error) {
	var inputs []contribution.Input
	for _, id := range eff.LoadedUnitIDs() {
		u := eff.Loaded[id]
		kind, ok := contribution.KindForUnitRoot(u.Kind)
		if !ok {
			continue
		}
		origin := ""
		if path, ok := eff.UnitPath(id); ok {
			origin = path.String()
		}
		inputs = append(inputs, contribution.Input{
			UnitID:         id,
			Kind:           kind,
			ProviderPackID: u.WinnerPackID,
			Body:           u.Content,
			Origin:         origin,
		})
	}
	return contribution.Compile(contribution.CompileInput{
		Units:         inputs,
		Configuration: eff.Desired.Configuration,
		UnitProvider: func(id string) (string, bool) {
			_, provider, ok := eff.UnitContent(id)
			return provider, ok
		},
		PackPresent:   eff.PackContributed,
		PackDependsOn: eff.PackDependsOn,
		ProviderRank: func(packID string) contribution.ProviderRank {
			// Stock rank follows the bundled trust root.
			if eff.StockAuthority(packID) {
				return contribution.RankStock
			}
			return contribution.RankDevice
		},
	})
}

// validatePersonaCoverage checks worker personas; coordinators use surface templates.
func validatePersonaCoverage(contract *prompts.PersonaContract, profiles []agentdef.Profile) error {
	var ids []string
	for _, p := range profiles {
		if slices.Contains(p.TopologyRoles, agentdef.TopologyRoleCoordinator) {
			continue
		}
		ids = append(ids, p.ID)
	}
	return prompts.ValidatePersonaContractCoverage(contract, ids)
}

// checkToolVocabulary resolves the tool names this catalog's packs author and
// the remedies their rules state.
func checkToolVocabulary(
	eff *extpacks.EffectiveCatalog,
	toolProfiles []sandbox.ToolProfile,
	agentProfiles []agentdef.Profile,
	rules *oar.RuleSet,
) ([]string, error) {
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, err
	}
	catalog, err := toolvocab.NewCatalog(schemas, toolProfiles, unitProvenance(eff))
	if err != nil {
		return nil, err
	}
	if err := toolvocab.ValidateProfiles(catalog, toolProfiles); err != nil {
		return nil, err
	}
	agents, surfaces, err := toolvocab.AgentSurfaces(agentProfiles, toolProfiles)
	if err != nil {
		return nil, err
	}
	notes, err := toolvocab.CheckRules(catalog, surfaces, rules)
	if err != nil {
		return nil, err
	}
	// Malformed skills already surface as catalog diagnostics; this reads the
	// ones that loaded.
	loaded, _ := extpacks.LoadEffectiveSkills(eff)
	return notes, toolvocab.ValidateSkills(catalog, agents, loaded)
}

// unitProvenance answers which pack authored a unit. Bundled content reports ""
// so stock reads as one authority however many leaves ship it.
func unitProvenance(eff *extpacks.EffectiveCatalog) toolvocab.Provenance {
	author := func(unitID string) string {
		u, ok := eff.Loaded[unitID]
		if !ok || eff.StockAuthority(u.WinnerPackID) {
			return ""
		}
		return u.WinnerPackID
	}
	return toolvocab.Provenance{
		ToolSchema:  func(tool string) string { return author("tools/schemas/" + tool) },
		ToolProfile: func(id string) string { return author("tools/profiles/" + id) },
		Rule:        func(id string) string { return author("policy/" + id) },
	}
}
