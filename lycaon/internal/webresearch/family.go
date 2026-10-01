package webresearch

import (
	"fmt"
	"strings"
)

// Provider family values for catalog-driven registration (runtime only).
const (
	FamilyPackageRegistry = "package_registry"
	FamilyDiscourse       = "discourse"
	FamilyStackExchange   = "stackexchange"
	FamilyMediawikiREST   = "mediawiki_rest"
	FamilyMediawikiAction = "mediawiki_action"
	FamilyCustom          = "custom"
)

// EffectiveFamily returns the registration family, defaulting omitted family to custom.
func (e CatalogEntry) EffectiveFamily() string {
	f := strings.TrimSpace(e.Family)
	if f == "" {
		return FamilyCustom
	}
	return f
}

func validateCatalogFamily(id string, entry CatalogEntry) error {
	family := entry.EffectiveFamily()
	switch family {
	case FamilyCustom, FamilyDiscourse, FamilyMediawikiREST, FamilyMediawikiAction:
		return nil
	case FamilyStackExchange:
		if strings.TrimSpace(entry.FamilyParams["site"]) == "" {
			return fmt.Errorf("web research catalog: %q family stackexchange requires family_params.site", id)
		}
		return nil
	case FamilyPackageRegistry:
		shape := strings.TrimSpace(entry.FamilyParams["shape"])
		if shape == "" {
			return fmt.Errorf("web research catalog: %q family package_registry requires family_params.shape", id)
		}
		if _, ok := packageRegistryShapes[shape]; !ok {
			return fmt.Errorf("web research catalog: %q has unknown package_registry shape %q", id, shape)
		}
		return nil
	default:
		return fmt.Errorf("web research catalog: %q has unknown family %q", id, family)
	}
}
