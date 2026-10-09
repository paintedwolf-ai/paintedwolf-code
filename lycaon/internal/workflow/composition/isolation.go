package composition

import (
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"strings"
)

// manifestRequiresIsolation detects competing parallel writers.
func manifestRequiresIsolation(m workflowdef.Manifest, catalog *extpacks.EffectiveCatalog) bool {
	if topo := strings.TrimSpace(m.Topology); topo != "" {
		if patternRequiresIsolation(loadTopologyPattern(catalog, topo)) {
			return true
		}
	}
	for _, phase := range m.PhaseDefs {
		if len(phase.BindParallelGroup) >= 2 {
			return true
		}
	}
	return false
}

func loadTopologyPattern(catalog *extpacks.EffectiveCatalog, topologyID string) orchestration.TopologyPattern {
	spec, err := orchestration.TopologySpecForID(catalog, topologyID)
	if err != nil {
		return ""
	}
	return spec.Pattern
}

func patternRequiresIsolation(pattern orchestration.TopologyPattern) bool {
	switch pattern {
	case orchestration.TopologyPack, orchestration.TopologyFanOut:
		return true
	default:
		return false
	}
}
