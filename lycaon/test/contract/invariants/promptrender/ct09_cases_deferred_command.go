package promptrender

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// Worker profiles with deferred command (tools: command: true), exercising
// validation-execution's can_request_command_verify arms and skill delivery
// when command must be loaded via request_tools before use.
var _ = hoststate.AddCustomizationCases(deferredCommandWorkerCases()...)

func deferredCommandWorkerCases() []hoststate.CustomizationCase {
	withScratch := overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false)
	withScratch.TempState = hoststate.TempScratchOnly
	return []hoststate.CustomizationCase{
		{
			ID: "own-implement-deferred-command-with-verify", Packs: []string{deferredCommandFixture("packs", "implement-deferred-command-with-verify")},
			Own:          map[string]string{"tools/profiles/implement": "fixture/implement-deferred-command-with-verify"},
			WorkerAgents: []string{"implementer"},
			Workers:      []hoststate.WorkerSeed{withScratch},
		},
		{
			ID: "own-implement-deferred-command", Packs: []string{deferredCommandFixture("packs", "implement-deferred-command")},
			Own:          map[string]string{"tools/profiles/implement": "fixture/implement-deferred-command"},
			WorkerAgents: []string{"implementer"},
			Workers:      []hoststate.WorkerSeed{overrideWorkerSeed(hoststate.WorkerLoadingStickyOnly, false)},
		},
	}
}

func deferredCommandFixture(parts ...string) string {
	return filepath.Join(append([]string{configlayout.FindModuleRoot(), "test", "contract", "invariants", "promptrender", "testdata", "ct09-deferred-command"}, parts...)...)
}
