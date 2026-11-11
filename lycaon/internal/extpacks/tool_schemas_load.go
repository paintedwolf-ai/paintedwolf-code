package extpacks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolschema"
)

const (
	toolSchemaUnitPrefix      = "tools/schemas/"
	operationSchemaUnitPrefix = "tools/operation-schemas/"
)

// LoadEffectiveToolSchemas parses the schemas of tools an agent can call.
func LoadEffectiveToolSchemas(eff *EffectiveCatalog) (*toolschema.Config, []Diagnostic, error) {
	return loadSchemaUnits(eff, toolSchemaUnitPrefix)
}

// LoadEffectiveRegistrySchemas parses every schema the registry declares: callable
// tools and the native operations command reaches.
func LoadEffectiveRegistrySchemas(eff *EffectiveCatalog) (*toolschema.Config, error) {
	callable, _, err := loadSchemaUnits(eff, toolSchemaUnitPrefix)
	if err != nil {
		return nil, err
	}
	operations, _, err := loadSchemaUnits(eff, operationSchemaUnitPrefix)
	if err != nil {
		return nil, err
	}
	return toolschema.Merge(callable, operations)
}

func loadSchemaUnits(eff *EffectiveCatalog, prefix string) (*toolschema.Config, []Diagnostic, error) {
	if eff == nil {
		return nil, nil, fmt.Errorf("tool schemas: effective catalog required")
	}
	entries := make(map[string]toolschema.Entry)
	// Missing schemas remove custom copy; malformed winning schemas fail the load.
	diags := unloadedToolSchemaDiags(eff)
	failed := false
	for _, id := range eff.LoadedUnitIDs() {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		u, ok := eff.Loaded[id]
		if !ok {
			continue
		}
		stem := strings.TrimPrefix(id, prefix)
		if !toolschema.ValidToolName(stem) {
			failed = true
			diags = append(diags, Diagnostic{
				Code:    DiagToolSchemaInvalid,
				UnitID:  id,
				PackID:  u.WinnerPackID,
				Message: fmt.Sprintf("invalid tool schema stem %q", stem),
			})
			continue
		}
		entry, err := toolschema.ParseEntry(u.Content)
		if err != nil {
			failed = true
			diags = append(diags, Diagnostic{
				Code:    DiagToolSchemaInvalid,
				UnitID:  id,
				PackID:  u.WinnerPackID,
				Message: err.Error(),
			})
			continue
		}
		entries[stem] = entry
	}
	if failed {
		return nil, diags, fmt.Errorf("%s: %s", strings.TrimSuffix(prefix, "/"), describeDiagnostics(diags))
	}
	return toolschema.ConfigFromEntries(entries), diags, nil
}

// unloadedToolSchemaDiags reports schema units excluded by resolution.
func unloadedToolSchemaDiags(eff *EffectiveCatalog) []Diagnostic {
	var diags []Diagnostic
	for id, u := range eff.Units {
		if !strings.HasPrefix(id, toolSchemaUnitPrefix) {
			continue
		}
		if len(u.Contributions) == 0 || eff.HasLoaded(id) {
			continue
		}
		packs := make([]string, 0, len(u.Contributions))
		for _, p := range u.Contributions {
			packs = append(packs, p.PackID)
		}
		diags = append(diags, Diagnostic{
			Code:   DiagToolSchemaMissing,
			UnitID: id,
			Message: fmt.Sprintf("tool schema unit %s is %s (contributions: %s); the tool would ship with no description or parameter schema",
				id, u.Status, strings.Join(packs, ", ")),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].UnitID < diags[j].UnitID })
	return diags
}
