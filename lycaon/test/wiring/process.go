package wiring

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/llm"
)

// hostProcessEnvironment is what every host reads from the process rather than
// from its configuration.
var hostProcessEnvironment = map[string]string{
	"LYCAON_LLM_MOCK":  "1",
	"LYCAON_API_TOKEN": api.TestAPIToken,
	"LYCAON_TEST":      "1",
	"LYCAON_LOG_LEVEL": "error",
}

// stubMCPDistro replaces the shipped MCP distro; each test supplies its own
// server catalog.
var stubMCPDistro = map[config.Rel]string{
	config.DistroMCP: "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: false\n",
}

var hostProcessPrepared atomic.Bool

// PrepareHostProcess sets the environment and bundled source every host in
// this test process shares. Call it from TestMain before any test runs; the
// bundled source then stays one generation, so the stock pack inventory is
// read once per process.
func PrepareHostProcess() {
	for key, value := range hostProcessEnvironment {
		if err := os.Setenv(key, value); err != nil {
			panic("set " + key + ": " + err.Error())
		}
	}
	config.UseFS(configtest.OverlayFS(stubMCPDistro))
	hostProcessPrepared.Store(true)
}

// runHostTests prepares the host process, runs the package's tests and then
// fails the package when the hosts they closed stay reachable.
func runHostTests(m *testing.M) int {
	PrepareHostProcess()
	return runReleasingHosts(m)
}

func requirePreparedHostProcess(t *testing.T) {
	t.Helper()
	if !hostProcessPrepared.Load() {
		t.Fatal("hosts need a prepared process: call wiring.Main or wiring.PrepareHostProcess from TestMain")
	}
}

var mockModelConfig = sync.OnceValues(llm.LoadMockConfig)

// loadMockConfig returns the bundled mock model script, parsed once per
// process. Providers copy it and never write to it.
func loadMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := mockModelConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}
