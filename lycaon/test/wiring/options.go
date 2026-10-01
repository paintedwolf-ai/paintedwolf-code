package wiring

import (
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

type options struct {
	llmClient               modelcall.LLMClient
	decider                 decide.Decider
	recording               bool
	autoCompleteDelegation  bool
	replaceManifests        map[string]workflowdef.Manifest
	useBundledScanners      bool
	coordinatorLoopDisabled bool
	productionCostPricer    bool
	secretMatcher           *secretmatch.Matcher
	mcpConnector            mcp.SessionConnector
	installedPackDirs       []string
}

// A scripted model calls surface tools outright, so the default decider
// loads every unit a turn could carry, the way a trained engine would for the
// request the script models. WithDecider(decide.Absent{}) restores the
// engine-less product default for tests that exercise loading itself.
func defaultOptions() options {
	return options{decider: decidetest.LoadAll{}}
}

// WithDecider injects the decision engine the session manager consults.
func WithDecider(d decide.Decider) Option {
	return func(o *options) {
		o.decider = d
	}
}

// Option configures BuildForTest.
type Option func(*options)

// Packs are installed into the isolated extension cache before app construction.
func WithInstalledPack(dirs ...string) Option {
	return func(o *options) { o.installedPackDirs = append(o.installedPackDirs, dirs...) }
}

// WithRecordingLLM wraps the mock provider in a RecordingClient exposed on Harness.Recording.
func WithRecordingLLM() Option {
	return func(o *options) {
		o.recording = true
	}
}

// WithLLMClient injects a custom LLM client via app.Config.TestLLMClient.
func WithLLMClient(client modelcall.LLMClient) Option {
	return func(o *options) {
		o.llmClient = client
	}
}

// WithSecretMatcher injects a test outbound secret matcher (e.g. NewInertMatcher).
func WithSecretMatcher(m *secretmatch.Matcher) Option {
	return func(o *options) {
		o.secretMatcher = m
	}
}

// WithMCPConnector replaces the default MockConnector used by BuildForTest.
func WithMCPConnector(c mcp.SessionConnector) Option {
	return func(o *options) {
		o.mcpConnector = c
	}
}

// WithProductionCostPricer selects pricing from device settings.
func WithProductionCostPricer() Option {
	return func(o *options) {
		o.productionCostPricer = true
	}
}

// WithManifestRegistry replaces the bundled workflow manifest registry.
func WithManifestRegistry(manifests map[string]workflowdef.Manifest) Option {
	return func(o *options) {
		o.replaceManifests = manifests
	}
}

// WithAutoCompleteDelegation records dispatched leg outcomes automatically.
func WithAutoCompleteDelegation() Option {
	return func(o *options) {
		o.autoCompleteDelegation = true
	}
}

// WithBundledScanners uses the bundled registry in security end-to-end tests.
func WithBundledScanners() Option {
	return func(o *options) {
		o.useBundledScanners = true
	}
}

// Disabling host wakes lets fixtures drive each coordinator turn explicitly.
func WithoutCoordinatorLoop() Option {
	return func(o *options) {
		o.coordinatorLoopDisabled = true
	}
}
