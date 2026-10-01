package extpacks

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmint"
)

// CredentialSlotsKindRoot contains additive, provider-scoped recognition units.
const CredentialSlotsKindRoot = "host/credential-slots"

// LoadEffectiveCredentialSlots reads only the winning catalog bytes.
func LoadEffectiveCredentialSlots(eff *EffectiveCatalog) ([]secretmint.Contribution, []Diagnostic, error) {
	var out []secretmint.Contribution
	var diags []Diagnostic
	for _, id := range eff.LoadedUnitIDs() {
		if !strings.HasPrefix(id, CredentialSlotsKindRoot+"/") {
			continue
		}
		body, provider, _ := eff.UnitContent(id)
		c, err := secretmint.ParseContribution(body)
		if err != nil {
			diags = append(diags, Diagnostic{Code: DiagCredentialSlotsInvalid, UnitID: id, PackID: provider, Message: err.Error()})
			continue
		}
		out = append(out, c)
	}
	if len(diags) > 0 {
		return nil, diags, fmt.Errorf("credential slots: %s", describeDiagnostics(diags))
	}
	return out, nil, nil
}
