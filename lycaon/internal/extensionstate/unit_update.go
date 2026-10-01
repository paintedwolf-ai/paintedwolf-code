package extensionstate

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
)

func (op UpdateUnitOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	mutation := extpacks.DesiredMutation{}
	if op.Enabled != nil {
		if *op.Enabled {
			mutation.EnableUnit = op.UnitID
		} else {
			mutation.DisableUnit = op.UnitID
		}
	}
	if op.OwnSet {
		if strings.TrimSpace(op.OwnPackID) == "" {
			mutation.ClearOwn = op.UnitID
		} else {
			mutation.OwnUnit, mutation.OwnPack = op.UnitID, strings.TrimSpace(op.OwnPackID)
		}
	}
	prep := mutationPrep(mutation)
	if mutation.OwnPack != "" {
		prep.subjectPacks = []string{mutation.OwnPack}
	}
	return prep, nil
}

func (op UpdateUnitOp) checkAgainst(scope Scope, eff *extpacks.EffectiveCatalog) error {
	if op.Enabled != nil {
		if err := (SetUnitDisabledOp{UnitID: op.UnitID, Disabled: !*op.Enabled}).checkAgainst(scope, eff); err != nil {
			return err
		}
	}
	if op.OwnSet {
		var pack *string
		if id := strings.TrimSpace(op.OwnPackID); id != "" {
			pack = &id
		}
		return (SetUnitOwnOp{UnitID: op.UnitID, PackID: pack}).checkAgainst(scope, eff)
	}
	return requireKnownUnit(eff, op.UnitID)
}
