package inject

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
)

// RenderToolProceduresBlock renders the procedure units that follow the
// provider's actual schema set, not reachability. omitted names units the
// turn decision left out.
func RenderToolProceduresBlock(
	ctx context.Context, renderer *prompts.InjectRenderer,
	sessionID, profileID string, offered []string, omitted map[string]bool,
) (string, error) {
	names := append([]string(nil), offered...)
	sort.Strings(names)
	vars := prompts.VisibleToolsNativePartialVars(names)
	vars["offered_tools"] = names
	mergeRunnerCapabilityVars(names, vars)
	prompts.MergeHTTPActionVars(names, vars)
	surface := "worker"
	host := promptunit.HostWorker
	if profileID == prompts.CoordinatorProfileID {
		surface = "coordinator"
		host = promptunit.HostCoordinator
	}
	catalog, err := renderer.UnitCatalog()
	if err != nil {
		return "", err
	}
	blocks, err := prompts.RenderUnitSlots(ctx, renderer, catalog, prompts.UnitSelectionVars(host, "", nil, names, omitted), vars)
	if err != nil {
		return "", err
	}
	prompts.MergeUnitVars(vars, blocks)
	block, err := anchor.RenderInform(ctx, anchor.InjectToolProcedures,
		anchor.MatchContext{Surface: surface, Profile: profileID, SessionID: sessionID}, renderer, vars)
	return strings.TrimSpace(block), err
}
