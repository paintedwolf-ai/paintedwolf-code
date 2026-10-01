package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/editorturn"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// The scope must reach the compiled contract, and a call from the wrong shape
// must land on a structured reject rather than a bare error.
func TestSessionScopedToolsCompileAndRejectByCode(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	if len(cfg.SessionScope) == 0 {
		t.Fatal("session_scope is empty; the host would offer every tool in both session shapes")
	}
	hints, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)

	for tool, scope := range cfg.SessionScope {
		contract, ok := toolcontract.Lookup(tool)
		if !ok {
			t.Errorf("%s: declares session_scope but has no compiled contract", tool)
			continue
		}
		if string(contract.SessionScope) != scope {
			t.Errorf("%s: contract scope %q != manifest %q — run ./task codegen:native-tool-contracts",
				tool, contract.SessionScope, scope)
			continue
		}
		worker := scope == string(toolcontract.SessionScopeWorkerChild)
		if contract.AdmitsSession(worker) == contract.AdmitsSession(!worker) {
			t.Errorf("%s: scope %q admits both session shapes", tool, scope)
		}
		if !rejectCodeNamesTool(hints, tool) {
			t.Errorf("%s: no registered reject code names it; an out-of-scope call would "+
				"refuse with a bare error and no branch", tool)
		}
	}
}

func rejectCodeNamesTool(hints *guidance.HintConfig, tool string) bool {
	if hints == nil {
		return false
	}
	for _, entry := range hints.HintCodes {
		for _, selected := range entry.Tools {
			if selected == tool {
				return true
			}
		}
	}
	return false
}

// A grant only bites in sessions the profile actually runs in. Granting a
// worker-scoped tool on a profile that never runs as a spawned child — or the
// reverse — is config that can never fire.
func TestSessionScopedGrantsCanFire(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	worker, addressed := profilesBySessionShape(t)

	for _, profile := range profiles {
		for tool, granted := range profile.Tools {
			if !granted {
				continue
			}
			switch cfg.SessionScope[tool] {
			case string(toolcontract.SessionScopeWorkerChild):
				if !worker[profile.ID] {
					t.Errorf("%s grants %s (worker_child) but no agent binds that profile",
						profile.ID, tool)
				}
			case string(toolcontract.SessionScopeAddressed):
				if !addressed[profile.ID] {
					t.Errorf("%s grants %s (addressed_session) but no coordinator agent or editor preset binds that profile",
						profile.ID, tool)
				}
			}
		}
	}
}

// profilesBySessionShape splits profiles by the role each agent declares: a
// worker agent is spawned as a child, a coordinator agent and the editor presets
// bind the session the user addressed. A posture binds no profile.
func profilesBySessionShape(t *testing.T) (worker, addressed map[string]bool) {
	t.Helper()
	worker = map[string]bool{}
	addressed = map[string]bool{}

	agents := toolfixture.LoadBundledAgentRegistry(t)
	for _, agent := range agents.List() {
		id := agent.ToolProfile
		if id == "" {
			continue
		}
		for _, role := range agent.TopologyRoles {
			switch role {
			case agentdef.TopologyRoleWorker:
				worker[id] = true
			case agentdef.TopologyRoleCoordinator:
				addressed[id] = true
			}
		}
	}
	for _, id := range editorturn.ToolProfileIDs() {
		addressed[id] = true
	}
	if len(worker) == 0 || len(addressed) == 0 {
		t.Fatalf("no profiles resolved: worker=%v addressed=%v", sortedKeys(worker), sortedKeys(addressed))
	}
	return worker, addressed
}

// Every dispatchable worker must be able to return the closeout the host requests.
func TestWorkerProfilesOfferStructuredCompletion(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	byID := map[string]sandbox.ToolProfile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	agents := toolfixture.LoadBundledAgentRegistry(t)
	checked := 0
	for _, agent := range agents.List() {
		if !slices.Contains(agent.TopologyRoles, agentdef.TopologyRoleWorker) {
			continue
		}
		checked++
		profile, ok := byID[agent.ToolProfile]
		if !ok {
			t.Errorf("worker %s has missing profile %s", agent.ID, agent.ToolProfile)
			continue
		}
		if !profile.ToolAllowed("complete_leg") || !profile.ToolSticky("complete_leg") {
			t.Errorf("worker %s cannot complete its leg through profile %s", agent.ID, profile.ID)
		}
	}
	if checked == 0 {
		t.Fatal("no worker profiles checked")
	}
}
