package configuration

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/agentdef"
	"log/slog"
	"slices"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/spawn"
)

type Catalog struct {
	ModuleRoot string
	Effective  *extpacks.EffectiveCatalog
	ViewCache  *catalogview.Cache
	DeviceView *catalogview.View
}

func (b *Catalog) Load(ctx context.Context, logger *slog.Logger) error {
	b.ModuleRoot = configlayout.FindModuleRoot()
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("config dir: %w", err)
	}
	scanners := scan.RequirementChecker{ModuleRoot: b.ModuleRoot, HomeDir: homeDir}
	b.ViewCache = catalogview.NewCache(b.ModuleRoot, logger)
	spawn.SetContributedAgentsSource(contributedWorkerAgents)
	eff, DeviceView, err := extensionstate.PublishDeviceCatalog(ctx, b.ViewCache,
		scanners, logger)
	if err != nil {
		return fmt.Errorf("extension packs: %w", err)
	}
	b.Effective = eff
	b.DeviceView = DeviceView
	caps, err := promptattach.LoadCaps()
	if err != nil {
		return fmt.Errorf("prompt attachment caps: %w", err)
	}
	promptattach.Install(caps)
	providerwire.SetWireImageBound(caps.Transport.MaxImage)
	budgets, err := prompts.LoadPromptBudgets()
	if err != nil {
		return fmt.Errorf("prompt budgets: %w", err)
	}
	providerwire.SetPerceptionWindow(providerwire.PerceptionWindow{
		MaxToolImages: budgets.Perception.MaxToolImages,
		DropBatch:     budgets.Perception.DropBatch,
	})
	return nil
}

// contributedWorkerAgents lists contributed dispatchable workers.
func contributedWorkerAgents() []string {
	eff := extpacks.Active()
	if eff == nil {
		return nil
	}
	profiles, err := agentdef.LoadEffectiveWithCatalog(eff)
	if err != nil {
		return nil
	}
	var out []string
	for _, profile := range profiles {
		if !slices.Contains(profile.TopologyRoles, agentdef.TopologyRoleWorker) {
			continue
		}
		unit, ok := eff.Loaded["agents/"+profile.ID]
		if !ok || eff.StockAuthority(unit.WinnerPackID) {
			continue
		}
		out = append(out, profile.ID)
	}
	return out
}
