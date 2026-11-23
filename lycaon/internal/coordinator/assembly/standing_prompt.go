package assembly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

type standingProfileInputs struct {
	SurfaceID       string   `json:"surface_id"`
	SurfaceTemplate string   `json:"surface_template,omitempty"`
	ModeRefs        []string `json:"mode_refs,omitempty"`
}

type standingPromptInputs struct {
	PromptRevision    string                `json:"prompt_revision"`
	Profile           standingProfileInputs `json:"profile"`
	Vars              map[string]any        `json:"vars,omitempty"`
	RootCount         int                   `json:"root_count"`
	EffectiveAgents   []string              `json:"effective_agents,omitempty"`
	WebSearchEnabled  bool                  `json:"web_search_enabled"`
	LoadedTools       []string              `json:"loaded_tools,omitempty"`
	ExecutionMode     string                `json:"execution_mode,omitempty"`
	VerifyCommand     string                `json:"verify_command,omitempty"`
	ReaderFingerprint string                `json:"reader_fingerprint"`

	reader prompts.Reader
}

func (in standingPromptInputs) withReader(reader prompts.Reader) standingPromptInputs {
	in.reader = reader
	in.ReaderFingerprint = reader.Fingerprint()
	return in
}

func (in standingPromptInputs) cacheKey() (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("marshal standing prompt inputs: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func renderStandingPrompt(ctx context.Context, pe prompts.PromptTemplateEngine, in standingPromptInputs) (string, error) {
	if pe == nil {
		return "", errPromptEngineNotConfigured
	}
	occ := map[string]any{}
	copyPromptVarsSorted(occ, in.Vars)

	if err := capability.MergeForTurn(occ, in.reader, in.EffectiveAgents); err != nil {
		return "", fmt.Errorf("capability vars: %w", err)
	}

	if err := prompts.MergeCoordinatorPromptVars(
		in.Profile.SurfaceID,
		prompts.ExecutionModePromptTransition{},
		in.VerifyCommand,
		occ,
	); err != nil {
		return "", fmt.Errorf("surface prompt vars: %w", err)
	}

	policyVars := spawn.PolicyTemplateVars()
	keys := make([]string, 0, len(policyVars))
	for k := range policyVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, exists := occ[k]; !exists {
			occ[k] = policyVars[k]
		}
	}

	filterOccurrenceSkillsForReader(pe, in.reader, occ)
	surfaceVars, err := prompts.Vars(in.reader, occ)
	if err != nil {
		return "", fmt.Errorf("standing prompt vars: %w", err)
	}

	catalog, err := prompts.UnitCatalogForEngine(pe)
	if err != nil {
		return "", fmt.Errorf("unit catalog: %w", err)
	}
	blocks, err := prompts.RenderUnitSlots(ctx, prompts.UnitRendererFor(pe), catalog, in.reader.Selection(), surfaceVars)
	if err != nil {
		return "", fmt.Errorf("unit blocks: %w", err)
	}
	prompts.MergeUnitVars(surfaceVars, blocks)

	var parts []string
	for _, ref := range []string{surface.CoordinatorCoreTemplate} {
		chunk, err := pe.Render(ctx, ref, surfaceVars)
		if err != nil {
			return "", fmt.Errorf("render %q: %w", ref, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	for _, modeRef := range in.Profile.ModeRefs {
		ref := surface.ModeTemplateRef(modeRef)
		chunk, err := pe.Render(ctx, ref, surfaceVars)
		if err != nil {
			return "", fmt.Errorf("render %q: %w", ref, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if in.Profile.SurfaceTemplate != "" {
		chunk, err := pe.Render(ctx, in.Profile.SurfaceTemplate, surfaceVars)
		if err != nil {
			return "", fmt.Errorf("render %q: %w", in.Profile.SurfaceTemplate, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

var engineSkillsByNameCache = scopedstore.New[map[string]skills.Skill](16)

func skillsByNameForEngine(pe prompts.PromptTemplateEngine) map[string]skills.Skill {
	var eff *extpacks.EffectiveCatalog
	key := "active"
	if fe, ok := pe.(*prompts.FileTemplateEngine); ok && fe != nil {
		eff = fe.Layers().Catalog
		if eff != nil {
			key = fmt.Sprintf("cat:%p:%s", eff, fe.Revision())
		}
	}
	if cached, ok := engineSkillsByNameCache.Load(key); ok {
		return cached
	}
	if eff == nil {
		eff = extpacks.Active()
	}
	loaded, _ := extpacks.LoadEffectiveSkills(eff)
	byName := make(map[string]skills.Skill, len(loaded))
	for _, sk := range loaded {
		byName[sk.Name] = sk
	}
	if key != "active" || len(byName) > 0 {
		engineSkillsByNameCache.Store(key, byName)
	}
	return byName
}

// filterOccurrenceSkillsForReader narrows agent_skills to procedures whose
// required tools and operations the turn reader actually offers.
func filterOccurrenceSkillsForReader(pe prompts.PromptTemplateEngine, reader prompts.Reader, occ map[string]any) {
	if !reader.Valid() || occ == nil {
		return
	}
	raw, exists := occ["agent_skills"]
	if !exists || raw == nil {
		return
	}
	known := skillsByNameForEngine(pe)
	keepSkill := func(name, desc string) bool {
		name = strings.TrimSpace(name)
		if name == "" {
			return false
		}
		sk, ok := known[name]
		if !ok {
			sk = skills.Skill{Name: name, Description: strings.TrimSpace(desc)}
		} else if trimmed := strings.TrimSpace(desc); trimmed != "" {
			sk.Description = trimmed
		}
		return toolvocab.SkillToolsOfferedForReader(sk, reader)
	}
	switch rows := raw.(type) {
	case []map[string]any:
		kept := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			name, _ := row["name"].(string)
			desc, _ := row["description"].(string)
			if keepSkill(name, desc) {
				kept = append(kept, row)
			}
		}
		if len(kept) == 0 {
			delete(occ, "agent_skills")
			return
		}
		occ["agent_skills"] = kept
	case []prompts.AgentSkillView:
		kept := make([]prompts.AgentSkillView, 0, len(rows))
		for _, row := range rows {
			if keepSkill(row.Name, row.Description) {
				kept = append(kept, row)
			}
		}
		if len(kept) == 0 {
			delete(occ, "agent_skills")
			return
		}
		occ["agent_skills"] = kept
	}
}
