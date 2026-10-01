package extpacks

import "context"

func desiredWithExtensionPacks(ids ...string) DesiredState {
	desired := EmptyDesired()
	for _, id := range ids {
		desired.Packs = append(desired.Packs, DesiredPack{ID: id})
	}
	return desired
}

func resolveWithDesired(ctx context.Context, desired DesiredState, scanners ScannerRequirementChecker) (*EffectiveCatalog, error) {
	content, err := DiscoverAllContent(nil)
	if err != nil {
		return nil, err
	}
	return Resolve(ctx, ResolveInput{
		Packs:    content,
		Desired:  desired,
		Scanners: scanners,
	}), nil
}

func desiredWithDisabledPack(packID string) DesiredState {
	enabled := false
	return DesiredState{
		Format: DesiredFormat,
		Packs:  []DesiredPack{{ID: packID, Enabled: &enabled}},
		Own:    map[string]string{},
	}
}
