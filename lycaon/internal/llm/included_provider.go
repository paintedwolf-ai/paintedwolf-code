package llm

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
)

const IncludedProviderID = "nativegen"
const IncludedProviderLabel = "Painted Wolf Code (included)"

var ErrCannotRemoveIncludedProvider = errors.New("included provider cannot be removed")

func IsIncludedProvider(id string) bool {
	return strings.TrimSpace(id) == IncludedProviderID
}

// IncludedProviderEntry describes the bundled transport; discovery supplies capacity.
func IncludedProviderEntry() ProviderEntry {
	requiresKey := false
	return ProviderEntry{
		ID: IncludedProviderID, Kind: IncludedProviderID, Label: IncludedProviderLabel,
		EndpointStyle: EndpointStyleDerived, RequiresAPIKey: &requiresKey,
		LocalFree: true, Platforms: []string{PlatformmacOS, PlatformLinux},
	}
}

func (r *registryDiscovery) IncludedModelAvailable(ctx context.Context) bool {
	if r == nil {
		return false
	}
	result, refresh := r.modelSnapshot(ctx, IncludedProviderID)
	refresh()
	for _, model := range result.Models {
		if modelinfo.EquivalentID(IncludedProviderID, model.ID, nativegen.DefaultModelID) {
			return true
		}
	}
	return false
}
