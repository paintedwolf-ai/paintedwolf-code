package prompts

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// RenderPersona renders a runtime worker persona and enforces its final contract.
func RenderPersona(ctx context.Context, engine *FileTemplateEngine, agentID string, reader Reader, occurrence map[string]any) (string, error) {
	engine, id, ref, data, err := personaVars(ctx, engine, agentID, reader, occurrence)
	if err != nil {
		return "", err
	}
	body, err := engine.Render(ctx, ref, data)
	if err != nil {
		return "", err
	}
	// Artifact budgets catch authoring regressions before runtime expansion.
	violations, err := validatePersonaContractRender(id, body)
	if err != nil {
		return "", err
	}
	if len(violations) > 0 {
		return "", fmt.Errorf("persona %q final render invalid: %s", id, strings.Join(violations, ", "))
	}
	return body, nil
}

func personaVars(ctx context.Context, engine *FileTemplateEngine, agentID string, reader Reader, occurrence map[string]any) (*FileTemplateEngine, string, string, map[string]any, error) {
	if engine == nil {
		return nil, "", "", nil, fmt.Errorf("prompt engine not configured")
	}
	if !reader.Valid() {
		return nil, "", "", nil, fmt.Errorf("persona render requires a valid reader")
	}
	eff := engine.Layers().Catalog
	if eff == nil {
		var err error
		eff, err = extpacks.CatalogForConsumers()
		if err != nil {
			return nil, "", "", nil, fmt.Errorf("persona effective catalog: %w", err)
		}
	}
	engine = engine.WithEffectiveCatalog(eff)
	cfg, err := loadPersonaContractCached(eff)
	if err != nil {
		return nil, "", "", nil, err
	}
	id := strings.TrimSpace(agentID)
	def, ok := cfg.Agents[id]
	if !ok {
		return nil, "", "", nil, fmt.Errorf("%w: persona contract: agent %q not in contract", ErrUnknownAgent, id)
	}
	if err := requireArchetypePartials(engine.Layers(), def.Archetype, cfg.Archetypes[def.Archetype]); err != nil {
		return nil, "", "", nil, err
	}
	ref, err := TemplateRefForAgent(id)
	if err != nil {
		return nil, "", "", nil, err
	}
	occ := PersonaDataMap(def)
	for k, v := range occurrence {
		occ[k] = v
	}
	if err := MergeWorkerPolicyTemplateVars(id, occ); err != nil {
		return nil, "", "", nil, fmt.Errorf("agent spawn vars for %q: %w", id, err)
	}
	data, err := Vars(reader, occ)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("persona vars for %q: %w", id, err)
	}
	unitCatalog, err := UnitCatalogFor(eff)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("unit catalog for %q: %w", id, err)
	}
	blocks, err := RenderUnitSlots(ctx, engine, unitCatalog, reader.Selection(), data)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("units for %q: %w", id, err)
	}
	MergeUnitVars(data, blocks)
	return engine, id, ref, data, nil
}

// pathCache memoizes bounded loads keyed by catalog or policy fingerprint.
type pathCache[T any] struct {
	mu      sync.Mutex
	entries map[string]T
}

func (c *pathCache[T]) get(key string, load func(string) (T, error)) (T, error) {
	c.mu.Lock()
	if val, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return val, nil
	}
	c.mu.Unlock()

	val, err := load(key)
	if err != nil {
		return val, err
	}

	c.mu.Lock()
	if c.entries == nil || len(c.entries) >= 64 {
		c.entries = make(map[string]T, 16)
	}
	c.entries[key] = val
	c.mu.Unlock()
	return val, nil
}

var (
	personaContractCache pathCache[*PersonaContract]
	toolSchemaCache      pathCache[*toolschema.Config]
	hintCodeRowsCache    pathCache[map[string]hintCodeRow]
	hintPolicyCache      pathCache[[]hintregistry.Entry]
	toolProfilesCache    pathCache[[]sandbox.ToolProfile]
	readerFactVocabCache pathCache[ReaderFactVocabulary]
	templateVocabCache   pathCache[[]string]
)

func catalogCacheKey(eff *extpacks.EffectiveCatalog) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(eff.Revision)))
	for _, id := range eff.LoadedUnitIDs() {
		u := eff.Loaded[id]
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(u.WinnerPackID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(u.Content)
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func catalogRevisionKey(eff *extpacks.EffectiveCatalog) string {
	if eff == nil {
		return "nil"
	}
	if rev := strings.TrimSpace(eff.Revision); rev != "" {
		return fmt.Sprintf("fp:%s:%d", rev, len(eff.Loaded))
	}
	return catalogCacheKey(eff)
}

func loadHintPolicyCached(eff *extpacks.EffectiveCatalog) ([]hintregistry.Entry, error) {
	return hintPolicyCache.get(catalogRevisionKey(eff), func(string) ([]hintregistry.Entry, error) {
		return hintregistry.ListEffectiveWithCatalog(eff)
	})
}

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
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(revision)))
	for _, e := range entries {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.Code))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(e.Body)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(e.NextSteps)
	}
	key := "policy:" + strings.TrimSpace(revision) + ":" + strconv.FormatUint(h.Sum64(), 16)
	return hintCodeRowsCache.get(key, func(string) (map[string]hintCodeRow, error) {
		return LoadHintCodeRowsFromEntries(entries)
	})
}
