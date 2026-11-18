// Package promptrender renders the stock prompts once for every reader
// state, and holds the laws over what renders: every persona, template, kick
// and skill renders, fits its budget, keeps its prose intact, and names
// only what its reader can reach.
package promptrender

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// Stock is the stock catalog with the tool profiles and tool vocabulary a
// render reads.
type Stock struct {
	Catalog  *extpacks.EffectiveCatalog
	Profiles []sandbox.ToolProfile
	Schemas  *toolschema.Config
	Names    *toolvocab.Catalog
	// Site is the checkout's distribution prompt overlay, which a render
	// reads before the bundled templates.
	Site string
}

// Engine renders the stock templates under the checkout's site overlay.
func (s *Stock) Engine() *prompts.FileTemplateEngine {
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot(), Site: s.Site})
}

// LoadStock resolves the stock catalog with no device choices.
func LoadStock(ctx context.Context) (*Stock, error) {
	content, err := extpacks.DiscoverStockContent()
	if err != nil {
		return nil, err
	}
	eff := extpacks.Resolve(ctx, extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return nil, fmt.Errorf("load tool profiles: %w", err)
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, fmt.Errorf("load tool schemas: %w", err)
	}
	names, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	if err != nil {
		return nil, err
	}
	return &Stock{Catalog: eff, Profiles: profiles, Schemas: schemas, Names: names}, nil
}

// PersonaEngine renders personas and agent templates from the module.
func PersonaEngine() *prompts.FileTemplateEngine {
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
}

// NoFolderSurfaces are the coordinator surfaces that open with no folder
// attached.
var NoFolderSurfaces = []string{"implement_routing", "implement_investigate", "implement_synthesis"}

// Text is one rendered text.
type Text struct {
	Label    string
	Text     string
	Resource bool
}

// SkillRender is one stock skill rendered for one reader.
type SkillRender struct {
	Skill  skills.Skill
	Reader string
	State  prompts.Reader
	Parts  []Text
}

// RenderStockSkills renders every stock skill once for each reader that can
// select it: coordinator surfaces across 0, 1 and 2 roots, and every worker
// profile across branch and git states.
func RenderStockSkills(stock *Stock) ([]SkillRender, error) {
	loaded, err := StockSkills(stock)
	if err != nil {
		return nil, err
	}
	var out []SkillRender
	add := func(label string, reader prompts.Reader) error {
		held := reader.Held()
		for _, sk := range loaded {
			if sk.Project || !toolvocab.SkillToolsOffered(sk, held) {
				continue
			}
			parts, err := RenderSkill(sk, prompts.SkillVars(reader))
			if err != nil {
				return err
			}
			out = append(out, SkillRender{Skill: sk, Reader: label, State: reader, Parts: parts})
		}
		return nil
	}
	for _, roots := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(roots)
		if err != nil {
			return nil, err
		}
		for _, id := range sortedKeys(plans) {
			if roots == 0 && !slices.Contains(NoFolderSurfaces, id) {
				continue
			}
			plan := plans[id]
			loadedTools := map[string]bool{}
			for _, name := range plan.AddressableNames() {
				loadedTools[name] = true
			}
			reader, err := prompts.NewReader(prompts.ReaderInput{
				ProfileID: prompts.CoordinatorProfileID, SurfaceID: id, Profiles: stock.Profiles,
				Plan: plan, Loaded: loadedTools, RootCount: roots, HasGit: roots > 0, WebSearch: true,
			})
			if err != nil {
				return nil, fmt.Errorf("coordinator reader for %s: %w", id, err)
			}
			if err := add(fmt.Sprintf("surface %s at %d roots", id, roots), reader); err != nil {
				return nil, err
			}
		}
	}
	agents, err := agentdef.LoadEffectiveWithCatalog(stock.Catalog)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, agent := range agents {
		if agent.ID == "coordinator" || seen[agent.ToolProfile] || !slices.ContainsFunc(stock.Profiles, func(p sandbox.ToolProfile) bool { return p.ID == agent.ToolProfile }) {
			continue
		}
		seen[agent.ToolProfile] = true
		for _, onBranch := range []bool{false, true} {
			for _, hasGit := range []bool{false, true} {
				reader, err := prompts.NewReader(prompts.ReaderInput{
					ProfileID: agent.ToolProfile, Profiles: stock.Profiles,
					OnWorkerBranch: onBranch, HasGit: hasGit, RootCount: 1, WebSearch: true,
				})
				if err != nil {
					return nil, fmt.Errorf("worker reader for %s: %w", agent.ToolProfile, err)
				}
				if err := add(fmt.Sprintf("profile %s (on branch: %t, git: %t)", agent.ToolProfile, onBranch, hasGit), reader); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

// StockSkills loads the stock pack skills with their bundled resources; a
// skill diagnostic fails the load.
func StockSkills(stock *Stock) ([]skills.Skill, error) {
	loaded, diags := extpacks.LoadEffectiveSkills(stock.Catalog)
	if len(diags) > 0 {
		messages := make([]string, 0, len(diags))
		for _, diag := range diags {
			messages = append(messages, diag.UnitID+": "+diag.Message)
		}
		return nil, fmt.Errorf("stock skills carry diagnostics:\n%s", strings.Join(messages, "\n"))
	}
	if len(loaded) == 0 {
		return nil, fmt.Errorf("no stock skills loaded")
	}
	return loaded, nil
}

// RenderSkill renders a skill body and each bundled markdown resource the way
// a read delivers them, with the empty configuration map production keeps
// for packs without settings.
func RenderSkill(sk skills.Skill, vars map[string]any) ([]Text, error) {
	data := make(map[string]any, len(vars)+1)
	for name, value := range vars {
		data[name] = value
	}
	data["configuration"] = map[string]any{}
	render := func(text, label string, resource bool) (Text, error) {
		tpl, err := pongoplain.Compile(text)
		if err != nil {
			return Text{}, fmt.Errorf("compile %s: %w", label, err)
		}
		out, err := pongoplain.ExecuteDetached(tpl, data)
		if err != nil {
			return Text{}, fmt.Errorf("render %s: %w", label, err)
		}
		return Text{Label: label, Text: out, Resource: resource}, nil
	}
	body, err := render(sk.Body, sk.Name, false)
	if err != nil {
		return nil, err
	}
	parts := []Text{body}
	for _, rel := range sk.MarkdownResources() {
		raw, err := sk.ReadResource(rel)
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", sk.Name, rel, err)
		}
		part, err := render(string(raw), sk.Name+"/"+rel, true)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
