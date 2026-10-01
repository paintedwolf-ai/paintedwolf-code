package extpacks

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/mcp/bindings"
)

// LoadEffectiveBindings rejects the entire binding set on invalid content.
func LoadEffectiveBindings(eff *EffectiveCatalog) ([]bindings.Binding, []Diagnostic, error) {
	if eff == nil {
		return nil, nil, nil
	}
	var out []bindings.Binding
	var diags []Diagnostic
	seenKeys := map[string]string{} // field key → unit id
	failed := false
	for _, id := range eff.LoadedUnitIDs() {
		if !strings.HasPrefix(id, "mcp_bindings/") {
			continue
		}
		u, ok := eff.Loaded[id]
		if !ok {
			continue
		}
		stem := strings.TrimPrefix(id, "mcp_bindings/")
		b, err := bindings.LoadBytes(stem, u.Content)
		if err != nil {
			failed = true
			diags = append(diags, Diagnostic{
				Code:    DiagMCPBindingInvalid,
				UnitID:  id,
				PackID:  u.WinnerPackID,
				Message: err.Error(),
			})
			continue
		}
		dup := false
		for _, f := range b.Fields {
			if prev, ok := seenKeys[f.Key]; ok {
				failed = true
				dup = true
				diags = append(diags, Diagnostic{
					Code:    DiagMCPBindingDupKey,
					UnitID:  id,
					PackID:  u.WinnerPackID,
					Message: fmt.Sprintf("duplicate field key %q (also in %s)", f.Key, prev),
				})
			}
		}
		if dup {
			continue
		}
		for _, f := range b.Fields {
			seenKeys[f.Key] = id
		}
		out = append(out, b)
	}
	if failed {
		return nil, diags, fmt.Errorf("mcp_bindings: %s", describeDiagnostics(diags))
	}
	return out, diags, nil
}
