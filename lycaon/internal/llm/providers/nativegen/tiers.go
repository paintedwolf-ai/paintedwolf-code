package nativegen

import (
	_ "embed"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/pkg/api"
)

type tier struct {
	id, name                         string
	minimum, recommended, workingSet uint64
}

var tiers = [...]tier{
	{DefaultModelID, "Gemma 4 E4B", 8 << 30, 8 << 30, 5 << 30},
	{"gemma4-12b-paintedwolf", "Gemma 4 12B", 16 << 30, 24 << 30, 8 << 30},
	{"gemma4-26b-paintedwolf", "Gemma 4 26B-A4B", 24 << 30, 32 << 30, 15 << 30},
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
		row := api.LocalModelTier{ID: t.id, DisplayName: t.name, MinMemoryBytes: int(t.minimum)}
		if p != nil {
			pin, ok := p.pin(t.id)
			row.DownloadBytes = int(pin.TotalBytes)
			row.DownloadAvailable = ok
			if p.config.DeviceMemory != nil && p.config.HostMemory != nil {
				capacity := p.config.DeviceMemory.Snapshot().CapacityBytes
				if p.config.UnifiedMemory {
					capacity = min(capacity, p.config.HostMemory.Snapshot().CapacityBytes)
				}
				row.Eligible = p.config.PhysicalBytes >= t.minimum && capacity >= t.workingSet
				if row.Eligible && p.config.PhysicalBytes >= t.recommended {
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
