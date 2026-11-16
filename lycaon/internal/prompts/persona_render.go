package prompts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// PersonaVars builds the variable map RenderPersona hands the template engine.
func PersonaVars(ctx context.Context, engine *FileTemplateEngine, agentID string, reader Reader, occurrence map[string]any) (map[string]any, error) {
	_, _, _, data, err := personaVars(ctx, engine, agentID, reader, occurrence)
	return data, err
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

func loadHintPolicyCached(eff *extpacks.EffectiveCatalog) ([]hintregistry.Entry, error) {
	return hintPolicyCache.get(catalogCacheKey(eff), func(string) ([]hintregistry.Entry, error) {
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
	hintPolicyCache.reset()
	toolProfilesCache.reset()
	readerFactVocabCache.reset()
	templateVocabCache.reset()
}
