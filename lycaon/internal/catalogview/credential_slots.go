package catalogview

import (
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/secretmint"
)

func compileCredentialSlots(eff *extpacks.EffectiveCatalog) (*secretmint.Inspector, error) {
	units, diags, err := extpacks.LoadEffectiveCredentialSlots(eff)
	if err != nil {
		faults := make([]contribution.Fault, 0, len(diags))
		for _, d := range diags {
			faults = append(faults, contribution.Fault{PackID: d.PackID, UnitID: d.UnitID, Code: d.Code, Message: d.Message})
		}
		return nil, &contribution.CompileError{Faults: faults}
	}
	return secretmint.Compile(units)
}
