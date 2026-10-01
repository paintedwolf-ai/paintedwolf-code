package prompts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// RenderPersona renders a runtime worker persona and enforces its final contract.
func RenderPersona(ctx context.Context, engine *FileTemplateEngine, agentID string, extra map[string]any) (string, error) {
	return renderPersona(ctx, engine, agentID, extra, true)
}

// renderPersona composes a persona with optional validation.
func renderPersona(ctx context.Context, engine *FileTemplateEngine, agentID string, extra map[string]any, validate bool) (string, error) {
	if engine == nil {
		return "", fmt.Errorf("prompt engine not configured")
	}
	eff := engine.Layers().Catalog
	if eff == nil {
		var err error
		eff, err = extpacks.CatalogForConsumers()
		if err != nil {
			return "", fmt.Errorf("persona effective catalog: %w", err)
		}
	}
	engine = engine.WithEffectiveCatalog(eff)
	cfg, err := loadPersonaContractCached(eff)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(agentID)
	def, ok := cfg.Agents[id]
	if !ok {
		return "", fmt.Errorf("%w: persona contract: agent %q not in contract", ErrUnknownAgent, id)
	}
	ref, err := TemplateRefForAgent(id)
	if err != nil {
		return "", err
	}
	data := PersonaDataMap(def)
	if _, ok := extra["web_search_enabled"]; !ok {
		data["web_search_enabled"] = true
	}
	for k, v := range extra {
		data[k] = v
	}
	if err := MergeWorkerPolicyTemplateVars(id, data); err != nil {
		return "", fmt.Errorf("agent spawn vars for %q: %w", id, err)
	}
	toolProfile, err := ToolProfileForAgent(id)
	if err != nil {
		return "", fmt.Errorf("agent tool profile for %q: %w", id, err)
	}
	{
		schemas, err := loadToolSchemaCached(eff)
		if err != nil {
			return "", fmt.Errorf("agent tool schemas for %q: %w", id, err)
		}
		policy, err := hintregistry.ListEffectiveWithCatalog(eff)
		if err != nil {
			return "", fmt.Errorf("agent hint policy for %q: %w", id, err)
		}
		hints, err := loadHintCodeRowsCached(eff.Revision, policy)
		if err != nil {
			return "", fmt.Errorf("agent hint codes for %q: %w", id, err)
		}
		profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
		if err != nil {
			return "", fmt.Errorf("agent tool profiles for %q: %w", id, err)
		}
		var visible []string
		var loaded, omitted map[string]bool
		if extra != nil {
			if raw, ok := extra["visible_tools"].([]string); ok {
				visible = raw
			}
			if raw, ok := extra["loaded_tools"].(map[string]bool); ok {
				loaded = raw
			}
			if raw, ok := extra["omitted_units"].(map[string]bool); ok {
				omitted = raw
			}
		}
		if err := MergeAgentToolSurfaceVars(toolProfile, visible, hints, SurfaceTurn{Loaded: loaded, Schemas: schemas}, data, profiles); err != nil {
			return "", fmt.Errorf("agent tool surface for %q: %w", id, err)
		}
		offered, _ := data["agent_tool_names"].([]string)
		floor := make([]string, 0, len(offered))
		for _, name := range offered {
			if !loaded[name] {
				floor = append(floor, name)
			}
		}
		unitCatalog, err := UnitCatalogFor(eff)
		if err != nil {
			return "", fmt.Errorf("unit catalog for %q: %w", id, err)
		}
		blocks, err := RenderUnitSlots(ctx, engine, unitCatalog, UnitSelectionVars(promptunit.HostWorker, "", floor, offered, omitted), data)
		if err != nil {
			return "", fmt.Errorf("units for %q: %w", id, err)
		}
		MergeUnitVars(data, blocks)
	}
	body, err := engine.Render(ctx, ref, data)
	if err != nil {
		return "", err
	}
	if validate {
		// Artifact budgets catch authoring regressions before runtime expansion.
		violations, err := validatePersonaContractRender(id, body)
		if err != nil {
			return "", err
		}
		if len(violations) > 0 {
			return "", fmt.Errorf("persona %q final render invalid: %s", id, strings.Join(violations, ", "))
		}
	}
	return body, nil
}

// pathCache memoizes one load per active path.
type pathCache[T any] struct {
	mu     sync.Mutex
	key    string
	val    T
	err    error
	loaded bool
}

func (c *pathCache[T]) get(key string, load func(string) (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && c.key == key && c.err == nil {
		return c.val, nil
	}
	val, err := load(key)
	c.key, c.val, c.err, c.loaded = key, val, err, true
	return val, err
}

func (c *pathCache[T]) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	c.key, c.val, c.err, c.loaded = "", zero, nil, false
}

var (
	personaContractCache pathCache[*PersonaContract]
	toolSchemaCache      pathCache[*toolschema.Config]
	hintCodeRowsCache    pathCache[map[string]hintCodeRow]
)

// Rows come from the catalog, so the cache keys on its revision.
func loadPersonaContractCached(eff *extpacks.EffectiveCatalog) (*PersonaContract, error) {
	if eff == nil {
		return nil, fmt.Errorf("persona contract: effective catalog required")
	}
	revision := strings.TrimSpace(eff.Revision)
	if revision == "" {
		return nil, fmt.Errorf("persona contract: effective catalog revision required")
	}
	return personaContractCache.get("fp:"+revision, func(string) (*PersonaContract, error) {
		return LoadPersonaContractWithCatalog(eff)
	})
}

func loadToolSchemaCached(eff *extpacks.EffectiveCatalog) (*toolschema.Config, error) {
	if eff == nil {
		return nil, fmt.Errorf("tool schemas: effective catalog required")
	}
	revision := strings.TrimSpace(eff.Revision)
	if revision == "" {
		return nil, fmt.Errorf("tool schemas: effective catalog revision required")
	}
	key := "fp:" + revision
	return toolSchemaCache.get(key, func(string) (*toolschema.Config, error) {
		cfg, _, err := extpacks.LoadEffectiveToolSchemas(eff)
		return cfg, err
	})
}

func loadHintCodeRowsCached(revision string, entries []hintregistry.Entry) (map[string]hintCodeRow, error) {
	raw, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("hint policy cache key: %w", err)
	}
	sum := sha256.Sum256(raw)
	key := "policy:" + strings.TrimSpace(revision) + ":" + hex.EncodeToString(sum[:])
	return hintCodeRowsCache.get(key, func(string) (map[string]hintCodeRow, error) {
		return LoadHintCodeRowsFromEntries(entries)
	})
}

// ResetPersonaContractCache clears persona render caches.
func ResetPersonaContractCache() {
	personaContractCache.reset()
	toolSchemaCache.reset()
	hintCodeRowsCache.reset()
}
