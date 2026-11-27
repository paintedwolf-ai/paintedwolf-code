package promptrender

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// projectAgentPairing is one agent row of the project pack: a shipped persona
// template under a tool profile its stock agent does not run. Each pairing is
// the smallest one that gives a persona-path guard the truth value no stock
// agent reaches.
type projectAgentPairing struct {
	agent, template, profile string
}

var projectAgentPairings = []projectAgentPairing{
	// path-explorer's `elif profile_has_inspect_source` is false only without
	// command and inspect_source.
	{agent: "web-path-explorer", template: "agents/path-explorer.md", profile: "web_research"},
	// security_scan_survey's else arm needs no scan tools and no request_tools.
	{agent: "web-security-reviewer", template: "agents/security-reviewer.md", profile: "web_research"},
	// validation-execution reaches a worker without complete_leg on the
	// implement_shell include path only through a write profile without it.
	{agent: "editor-implementer", template: "agents/implementer.md", profile: "editor_file"},
}

// agentLoadedCase loads named deferred tools for a stock agent, as
// request_tools loads each schema it is asked for by name.
type agentLoadedCase struct {
	agent  string
	loaded []string
}

var agentLoadedCases = []agentLoadedCase{
	// security_scan_survey and security-scan-targeted-context guard on the
	// three scan tools together and scan_compare alone.
	{agent: "security-reviewer", loaded: []string{"scan_list", "scan_query", "scan_summary"}},
	{agent: "security-reviewer", loaded: []string{"scan_query", "scan_summary"}},
	{agent: "security-reviewer", loaded: []string{"scan_list", "scan_summary"}},
	{agent: "security-reviewer", loaded: []string{"scan_list", "scan_query"}},
	// tool-turn-batch guards on web_search or fetch_url.
	{agent: "implementer", loaded: []string{"fetch_url"}},
	{agent: "implementer", loaded: []string{"web_search"}},
}

const projectAgentsProjectID = "ct09-project"

func projectAgentPackRoot() string {
	return filepath.Join(configlayout.FindModuleRoot(), "test", "contract", "invariants", "promptrender", "testdata", "ct09-project-agents")
}

// projectAgentWorkerStates are the worker states of the project pack's agents
// and of the stock agents' loaded-tool cases.
var projectAgentWorkerStates = sync.OnceValues(func() ([]hoststate.WorkerState, error) {
	ctx := context.Background()
	co, err := hoststate.ProjectAgentOverlay(ctx, "project-agents", projectAgentPackRoot(), projectAgentsProjectID)
	if err != nil {
		return nil, err
	}
	if err := admitProjectAgents(co); err != nil {
		return nil, err
	}
	var out []hoststate.WorkerState
	for _, agent := range co.Workers {
		for _, seed := range projectAgentSeeds() {
			ws, err := hoststate.BuildWorkerState(ctx, co.Overlay, agent, seed)
			if err != nil {
				return nil, err
			}
			out = append(out, ws)
		}
	}
	overlays, err := hoststate.DiscoverPackOverlays(ctx)
	if err != nil {
		return nil, err
	}
	stock := overlays[0]
	for _, c := range agentLoadedCases {
		i := slices.IndexFunc(stock.WorkerAgents, func(a hoststate.WorkerAgent) bool { return a.ID == c.agent })
		if i < 0 {
			return nil, fmt.Errorf("loaded case: stock agent %s not found", c.agent)
		}
		ws, err := hoststate.BuildWorkerState(ctx, stock, stock.WorkerAgents[i], projectAgentSeeds()[0])
		if err != nil {
			return nil, err
		}
		ws, err = loadDeferredTools(ctx, ws, c.loaded)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, nil
})

// admitProjectAgents checks the pack's agent rows against the pairing table
// and runs boot's agent admission (orchestration.ValidateAgentToolProfiles and
// ValidateAgentSkillSurface) on the customized catalog, so no pairing renders
// that production would refuse.
func admitProjectAgents(co hoststate.CustomizationOverlay) error {
	defs, err := agentdef.LoadEffectiveWithCatalog(co.Overlay.Catalog)
	if err != nil {
		return err
	}
	reg := orchestration.NewMemoryAgentRegistry()
	for _, d := range defs {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	if err := orchestration.ValidateAgentToolProfiles(reg, co.Overlay.Profiles); err != nil {
		return err
	}
	byID := make(map[string]sandbox.ToolProfile, len(co.Overlay.Profiles))
	for _, p := range co.Overlay.Profiles {
		byID[p.ID] = p
	}
	if err := orchestration.ValidateAgentSkillSurface(reg, byID); err != nil {
		return err
	}
	var got []string
	for _, w := range co.Workers {
		got = append(got, w.ID+"|"+w.Profile.SystemPromptTemplate+"|"+w.ToolProfile)
	}
	var want []string
	for _, p := range projectAgentPairings {
		want = append(want, p.agent+"|"+p.template+"|"+p.profile)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("project pack agents %v do not match the pairing table %v", got, want)
	}
	return nil
}

// projectAgentSeeds meet each pairing in an Agent and an Ask parent, sticky
// and with every deferred tool loaded; BuildWorkerState excludes the
// combinations production refuses.
func projectAgentSeeds() []hoststate.WorkerSeed {
	base := hoststate.WorkerSeed{
		Posture: api.SessionPostureBuild, BranchView: hoststate.BranchMainGit, RootCount: 1,
		Loading: hoststate.WorkerLoadingStickyOnly, ParentSessionMode: api.SessionModeAgent,
		WebSearch: true, ApprovalPosture: gate.PostureBalanced, TempState: hoststate.TempNone,
	}
	ask := base
	ask.Posture, ask.ParentSessionMode = api.SessionPostureSpec, api.SessionModeAsk
	loaded := base
	loaded.Loading = hoststate.WorkerLoadingAllLoaded
	return []hoststate.WorkerSeed{base, ask, loaded}
}

// loadDeferredTools loads names into a sticky-only state; each must be one
// of the plan's deferred tools, which request_tools loads by name.
func loadDeferredTools(ctx context.Context, ws hoststate.WorkerState, names []string) (hoststate.WorkerState, error) {
	auth, err := hoststate.FreezeWorkerAuthority(ctx, ws)
	if err != nil {
		return hoststate.WorkerState{}, err
	}
	deferred := auth.Plan.DeferredNames()
	loaded := make(map[string]bool, len(names))
	for _, name := range names {
		if !slices.Contains(deferred, name) {
			return hoststate.WorkerState{}, fmt.Errorf("%s: %s is not a deferred tool request_tools can load", ws.ID, name)
		}
		loaded[name] = true
	}
	ws.Loaded = loaded
	ws.ID = strings.Replace(ws.ID, "/load="+string(ws.Loading)+"/", "/load="+string(hoststate.WorkerLoadingPartlyLoaded)+"/", 1) +
		"/loaded=" + strings.Join(names, "+")
	ws.Loading = hoststate.WorkerLoadingPartlyLoaded
	return ws, nil
}
