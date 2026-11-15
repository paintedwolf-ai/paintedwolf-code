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
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
)

type standingProfileInputs struct {
	SurfaceID       string   `json:"surface_id"`
	SurfaceTemplate string   `json:"surface_template,omitempty"`
	ModeRefs        []string `json:"mode_refs,omitempty"`
}

type standingPromptInputs struct {
	PromptRevision    string                 `json:"prompt_revision"`
	Profile           standingProfileInputs  `json:"profile"`
	Vars              map[string]any         `json:"vars,omitempty"`
	RootCount         int                    `json:"root_count"`
	EffectiveAgents   []string               `json:"effective_agents,omitempty"`
	WebSearchEnabled  bool                   `json:"web_search_enabled"`
	LoadedTools       []string               `json:"loaded_tools,omitempty"`
	ExecutionMode     string                 `json:"execution_mode,omitempty"`
	VerifyCommand     string                 `json:"verify_command,omitempty"`
	WorkerToolBudget  spawn.WorkerToolBudget `json:"worker_tool_budget"`
	ReaderFingerprint string                 `json:"reader_fingerprint"`

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
	surfaceVars := in.reader.Facts()
	copyPromptVarsSorted(surfaceVars, in.Vars)
	surfaceVars["root_count"] = in.reader.RootCount()
	if readable, _ := surfaceVars["profile_has_skills_read"].(bool); !readable {
		delete(surfaceVars, "agent_skills")
	}

	if err := capability.MergeForTurn(surfaceVars, in.reader, in.EffectiveAgents); err != nil {
		return "", fmt.Errorf("capability vars: %w", err)
	}

	if err := prompts.MergeCoordinatorPromptVars(
		in.Profile.SurfaceID,
		prompts.ExecutionModePromptTransition{ExecutionMode: in.ExecutionMode},
		in.VerifyCommand,
		surfaceVars,
	); err != nil {
		return "", fmt.Errorf("surface prompt vars: %w", err)
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

	policyVars := spawn.PolicyTemplateVars(in.WorkerToolBudget)
	keys := make([]string, 0, len(policyVars))
	for k := range policyVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, exists := surfaceVars[k]; !exists {
			surfaceVars[k] = policyVars[k]
		}
	}

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
