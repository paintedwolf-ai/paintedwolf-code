package configuration

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/decide"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/startupprotocol"
)

// Config holds serve-time options for app.Build.
type Config struct {
	ConfigRoot  string
	DBPath      string
	ListenAddr  string
	MockLLMPath string
	// Startup reports bundled-engine boot progress to the desktop parent.
	Startup startupprotocol.Sink
	// TestLLMClient, when set, is used as the session LLM client instead of the bundled mock.
	TestLLMClient modelcall.LLMClient
	// TestDecider, when set, answers turn decisions instead of the engine the
	// environment resolves.
	TestDecider decide.Decider
	// TestMCPConnector, when set, avoids spawning real MCP subprocesses during app.Build.
	TestMCPConnector mcp.SessionConnector
	// TestMCPGlobalOverridePath isolates MCP enablement from the developer machine config.
	TestMCPGlobalOverridePath string
	// TestWorkflowTemplatesDir supplies compose templates in tests.
	TestWorkflowTemplatesDir string
	// TestScanRegistry, when set, replaces bundled scanners for deterministic scan tests.
	TestScanRegistry scan.CodeScannerRegistry
	// TestAdvisoryDatabase, when set, is the OSV export bundled dependency
	// scanners match against without contacting the advisory endpoint.
	TestAdvisoryDatabase string
	// TestCostPricer, when set, replaces NoopPricer in tests.
	TestCostPricer cost.Pricer
	// TestSessionLimits supplies session limits in tests.
	TestSessionLimits *settings.SessionLimits
	// TestSecretMatcher supplies outbound secret matching in tests.
	TestSecretMatcher *secretmatch.Matcher
	// TestOrchestrator, when set, builds the workflow orchestrator from the
	// production dependencies so tests can substitute leg dispatch.
	TestOrchestrator func(orchestration.OrchestratorDeps) orchestration.Orchestrator
}

func (c Config) ResolveListenAddr() (string, error) {
	if c.ListenAddr != "" {
		return c.ListenAddr, nil
	}
	addr, err := api.ResolveListenAddr()
	if err != nil {
		return "", fmt.Errorf("listen address: %w", err)
	}
	return addr, nil
}

func (c Config) ResolveDBPath() (string, error) {
	if c.DBPath != "" {
		return c.DBPath, nil
	}
	path, err := db.DefaultPath()
	if err != nil {
		return "", fmt.Errorf("database path: %w", err)
	}
	return path, nil
}
