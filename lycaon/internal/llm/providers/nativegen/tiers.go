package nativegen

import (
	_ "embed"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/pkg/api"
)

type tier struct {
	id, name, description                string
	minimum, recommended, workingSet     uint64
	discreteMinimum, discreteRecommended uint64
}

var tiers = [...]tier{
	{DefaultModelID, "Plain Bialy", "The smallest model. Leaves the most memory for other apps.", 8 << 30, 8 << 30, 5 << 30, 8 << 30, 8 << 30},
	{"gemma4-12b-paintedwolf", "Poppy Bialy", "A mid-sized model for everyday coding.", 16 << 30, 24 << 30, 8 << 30, 12 << 30, 16 << 30},
	{"gemma4-26b-paintedwolf", "Egg and Cheese Bialy", "The largest model, for workstations with more memory.", 24 << 30, 32 << 30, 15 << 30, 16 << 30, 24 << 30},
}

//go:embed release-pins.json
var releasePins []byte

func compiledPins() []modelstore.ModelPin {
	var pins []modelstore.ModelPin
	if err := json.Unmarshal(releasePins, &pins); err != nil {
		panic(err)
	}
	return pins
}

func KnownModel(id string) bool {
	for _, t := range tiers {
		if t.id == id {
			return true
		}
	}
	return false
}

func (p *Runtime) pin(model string) (modelstore.ModelPin, bool) {
	for _, pin := range p.config.Pins {
		if pin.Model == model && pin.Device == p.config.Backend {
			return pin, true
		}
	}
	return modelstore.ModelPin{}, false
}

func (p *Runtime) ModelTiers() []api.LocalModelTier {
	out := make([]api.LocalModelTier, 0, len(tiers))
	best := -1
	for _, t := range tiers {
		minimum, recommended := t.minimum, t.recommended
		if p != nil && p.config.Backend == "vulkan" && !p.config.UnifiedMemory {
			minimum, recommended = t.discreteMinimum, t.discreteRecommended
		}
		row := api.LocalModelTier{ID: t.id, DisplayName: t.name, Description: t.description, MinMemoryBytes: int(minimum)} // #nosec G115 -- Tier constants fit the supported 64-bit hosts.
		if p != nil {
			pin, ok := p.pin(t.id)
			row.DownloadBytes = int(pin.TotalBytes)
			row.DownloadAvailable = ok
			if p.config.DeviceMemory != nil && p.config.HostMemory != nil {
				capacity := p.config.DeviceMemory.Snapshot().CapacityBytes
				if p.config.UnifiedMemory {
					capacity = min(capacity, p.config.HostMemory.Snapshot().CapacityBytes)
				}
				row.Eligible = p.config.PhysicalBytes >= minimum && capacity >= t.workingSet
				if row.Eligible && p.config.PhysicalBytes >= recommended {
					best = len(out)
				}
			}
		}
		out = append(out, row)
	}
	if best >= 0 {
		out[best].Recommended = true
	}
	return out
}

func (p *Runtime) seedModel() string {
	if p.config.SeedDirectory == "" {
		return ""
	}
	slot, err := modelstore.BundleSlot(p.lifetime, p.config.SeedDirectory)
	if err != nil || slot.Device != p.config.Backend || !KnownModel(slot.Model) {
		return ""
	}
	return slot.Model
}
