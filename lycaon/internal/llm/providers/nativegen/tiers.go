package nativegen

import (
	_ "embed"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/pkg/api"
)

type tierMemory struct {
	minimum, recommended, workingSet uint64
}

type tier struct {
	id, name, description string
	unified, discrete     tierMemory
}

// The 44,800-token Vulkan plan includes weights, KV storage and workspace.
const largeVulkanWorkingSet = 16_646_471_680

var tiers = [...]tier{
	{DefaultModelID, "Small", "Leaves the most memory for your other apps.",
		tierMemory{8 << 30, 8 << 30, 5 << 30}, tierMemory{8 << 30, 8 << 30, 5 << 30}},
	{"gemma4-12b-paintedwolf", "Medium", "A balance of model size and memory use for everyday coding.",
		tierMemory{16 << 30, 24 << 30, 8 << 30}, tierMemory{12 << 30, 16 << 30, 8 << 30}},
	{"gemma4-26b-paintedwolf", "Large", "The most parameters, for devices with more memory.",
		tierMemory{24 << 30, 32 << 30, 15 << 30}, tierMemory{0, 24 << 30, largeVulkanWorkingSet}},
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
		requirements := t.unified
		if p != nil && p.config.Backend == "vulkan" && !p.config.UnifiedMemory {
			requirements = t.discrete
			requirements.minimum = max(requirements.minimum, requirements.workingSet+p.config.DecisionDeviceBytes)
		}
		row := api.LocalModelTier{ID: t.id, DisplayName: t.name, Description: t.description, MinMemoryBytes: int(requirements.minimum)} // #nosec G115 -- The memory plan fits the supported 64-bit hosts.
		if p != nil {
			pin, ok := p.pin(t.id)
			row.DownloadBytes = int(pin.TotalBytes)
			row.DownloadAvailable = ok
			if p.config.DeviceMemory != nil && p.config.HostMemory != nil {
				capacity := p.config.DeviceMemory.Snapshot().CapacityBytes
				if p.config.UnifiedMemory {
					capacity = min(capacity, p.config.HostMemory.Snapshot().CapacityBytes)
				}
				row.Eligible = p.config.PhysicalBytes >= requirements.minimum && capacity >= requirements.workingSet
				if row.Eligible && p.config.PhysicalBytes >= requirements.recommended {
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
