package extensionstate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
)

func (op InstallOp) prepare(ctx context.Context, owner *Owner, scope Scope) (*prepared, error) {
	plan, err := extpacks.PrepareInstall(ctx, extpacks.InstallOptions{
		Source: op.Source, Version: op.Version, Ref: op.Ref,
		ProjectDir:      scope.ProjectDir,
		ScannersEnabled: owner.Scanners,
	})
	if err != nil {
		return nil, err
	}
	if expected := strings.TrimSpace(op.ExpectedPackID); expected != "" && plan.Resolution.PackID != expected {
		plan.Close()
		return nil, fmt.Errorf("suggestion %s resolves to package %s", expected, plan.Resolution.PackID)
	}
	return &prepared{
		plan: plan, warnings: plan.Warnings,
		subjectPacks: []string{plan.Resolution.PackID},
	}, nil
}

func (op InstallMetaOp) prepare(ctx context.Context, _ *Owner, scope Scope) (*prepared, error) {
	plan, err := extpacks.PrepareInstallMeta(ctx, extpacks.InstallMetaOptions{
		Source: op.Source, Version: op.Version, Ref: op.Ref,
		ProjectDir: scope.ProjectDir,
	})
	if err != nil {
		return nil, err
	}
	subjects := []string(nil)
	if plan.Meta != nil {
		subjects = append([]string(nil), plan.Meta.MemberPackIDs...)
	}
	return &prepared{plan: plan, warnings: plan.Warnings, subjectPacks: subjects}, nil
}

func (op RemoveOp) prepare(_ context.Context, _ *Owner, _ Scope) (*prepared, error) {
	plan, err := extpacks.PrepareRemoval([]string{op.PackID})
	if err != nil {
		return nil, err
	}
	return &prepared{plan: plan}, nil
}

func (op RemoveMetaOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	return &prepared{cacheOnly: func() error { return extpacks.RemoveMetaPack(op.MetaPackID) }}, nil
}

func (op UpdateOp) prepare(ctx context.Context, _ *Owner, _ Scope) (*prepared, error) {
	plan, err := extpacks.PrepareUpdate(ctx, op.PackID)
	if err != nil {
		return nil, err
	}
	return &prepared{plan: plan, warnings: plan.Warnings, subjectPacks: []string{op.PackID}}, nil
}

func (op ReloadOp) prepare(ctx context.Context, _ *Owner, _ Scope) (*prepared, error) {
	plan, err := extpacks.PrepareReload(ctx, op.PackID)
	if err != nil {
		return nil, err
	}
	return &prepared{plan: plan, warnings: plan.Warnings, subjectPacks: []string{op.PackID}}, nil
}

func (LockOp) prepare(ctx context.Context, _ *Owner, _ Scope) (*prepared, error) {
	plan, err := extpacks.PrepareLockDesired(ctx)
	if err != nil {
		return nil, err
	}
	return &prepared{plan: plan}, nil
}

func (op SetPackEnabledOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	enabled := op.Enabled
	mutation := extpacks.DesiredMutation{
		SetPack: &extpacks.DesiredPack{ID: strings.TrimSpace(op.PackID), Enabled: &enabled},
	}
	prep := mutationPrep(mutation)
	// Validate enabled packs before publishing the mutation.
	if enabled {
		prep.subjectPacks = []string{strings.TrimSpace(op.PackID)}
	}
	return prep, nil
}

func (op SetUnitDisabledOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	mutation := extpacks.DesiredMutation{}
	if op.Disabled {
		mutation.DisableUnit = op.UnitID
	} else {
		mutation.EnableUnit = op.UnitID
	}
	return mutationPrep(mutation), nil
}

func (op SetUnitOwnOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	mutation := extpacks.DesiredMutation{}
	prep := mutationPrep(mutation)
	if op.PackID == nil {
		mutation.ClearOwn = op.UnitID
	} else {
		mutation.OwnUnit = op.UnitID
		mutation.OwnPack = *op.PackID
		// Choosing a winner is acting on the pack chosen.
		prep.subjectPacks = []string{strings.TrimSpace(*op.PackID)}
	}
	prep.mutate = mutationPrep(mutation).mutate
	return prep, nil
}

func (op ApplyProfileOp) prepare(_ context.Context, _ *Owner, _ Scope) (*prepared, error) {
	profile, err := extpacks.LoadDeviceProfile(op.PackID, op.Profile)
	if err != nil {
		return nil, err
	}
	return &prepared{
		mutate: func(desired extpacks.DesiredState) (extpacks.DesiredState, error) {
			return profile.ApplyTo(desired), nil
		},
		subjectPacks: []string{op.PackID},
	}, nil
}

func (op ApplyMetaOp) prepare(_ context.Context, _ *Owner, _ Scope) (*prepared, error) {
	mutation, err := extpacks.PrepareMetaMemberMutation(op.MetaPackID, op.Enable)
	if err != nil {
		return nil, err
	}
	return &prepared{
		mutate: func(desired extpacks.DesiredState) (extpacks.DesiredState, error) {
			return mutation.ApplyTo(desired), nil
		},
		warnings: mutation.Warnings,
	}, nil
}

func (op SetConfigurationOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	if len(op.Packs) == 0 {
		return nil, fmt.Errorf("configuration requires at least one pack block")
	}
	prep := mutationPrep(extpacks.DesiredMutation{SetConfiguration: op.Packs})
	// Configuration errors reject the whole save.
	for id := range op.Packs {
		prep.subjectPacks = append(prep.subjectPacks, strings.TrimSpace(id))
	}
	sort.Strings(prep.subjectPacks)
	return prep, nil
}

func (op UndeclineOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	return mutationPrep(extpacks.DesiredMutation{UndeclinePacks: op.PackIDs}), nil
}

func (op SetInstalledFromOp) prepare(context.Context, *Owner, Scope) (*prepared, error) {
	return mutationPrep(extpacks.DesiredMutation{
		SetPack: &extpacks.DesiredPack{ID: strings.TrimSpace(op.PackID), InstalledFrom: strings.TrimSpace(op.ProjectID)},
	}), nil
}

// checkAgainst rejects pack ids absent from the catalog.
func (op SetPackEnabledOp) checkAgainst(_ Scope, eff *extpacks.EffectiveCatalog) error {
	id := strings.TrimSpace(op.PackID)
	for _, p := range eff.Packs {
		if p.ID == id {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrUnknownPack, id)
}

func (op SetUnitDisabledOp) checkAgainst(scope Scope, eff *extpacks.EffectiveCatalog) error {
	if err := requireKnownUnit(eff, op.UnitID); err != nil {
		return err
	}
	id := strings.TrimSpace(op.UnitID)
	if scope.Kind == "project" && op.Disabled && !extpacks.ProjectDisableAllowed(
		extpacks.KindRootForUnitID(id), len(eff.InspectContributions(id)) > 0,
	) {
		return fmt.Errorf("%w: %s", ErrProjectUnitDisableUnsupported, id)
	}
	return nil
}

func (op SetUnitOwnOp) checkAgainst(_ Scope, eff *extpacks.EffectiveCatalog) error {
	if err := requireKnownUnit(eff, op.UnitID); err != nil {
		return err
	}
	if op.PackID == nil {
		return nil
	}
	unitID := strings.TrimSpace(op.UnitID)
	if extpacks.ProviderScoped(extpacks.KindRootForUnitID(unitID)) {
		return fmt.Errorf(
			"unit %s belongs to the pack that declares it, so there is no second contribution to choose between — disable it instead",
			unitID)
	}
	winner := strings.TrimSpace(*op.PackID)
	for _, c := range eff.InspectContributions(unitID) {
		if c.PackID == winner {
			return nil
		}
	}
	return fmt.Errorf("pack %s does not provide unit %s", winner, unitID)
}

// requireKnownUnit checks contributions because disabled ids get tracking rows.
func requireKnownUnit(eff *extpacks.EffectiveCatalog, unitID string) error {
	id := strings.TrimSpace(unitID)
	if id == "" {
		return fmt.Errorf("%w: (empty)", ErrUnknownUnit)
	}
	if len(eff.InspectContributions(id)) > 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnknownUnit, id)
}
