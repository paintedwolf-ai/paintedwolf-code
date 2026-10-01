// Package preflight reports host dependency readiness from machine state.
package preflight

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/userpath"
)

// Status is a probe verdict. There is no fourth state and no numeric score.
type Status string

const (
	// StatusOK means the dependency is present and usable.
	StatusOK Status = "ok"
	// StatusDegraded means a capability is unavailable; the app still works.
	StatusDegraded Status = "degraded"
	// StatusBlocked means the app cannot function.
	StatusBlocked Status = "blocked"
)

// severity ranks statuses so overall can be the worst seen.
func severity(s Status) int {
	switch s {
	case StatusBlocked:
		return 2
	case StatusDegraded:
		return 1
	default:
		return 0
	}
}

// Probe codes have matching user-notice catalog entries.
const (
	CodeOSBelowFloor              = "OS_BELOW_FLOOR"
	CodeConfigDirUnwritable       = "CONFIG_DIR_UNWRITABLE"
	CodeHostFileLimitReached      = "HOST_FILE_LIMIT_REACHED"
	CodeDiskSpaceLow              = "DISK_SPACE_LOW"
	CodeGitEngineUnavailable      = "GIT_ENGINE_UNAVAILABLE"
	CodeScannerEngineUnavailable  = "SCANNER_ENGINE_UNAVAILABLE"
	CodeBrowserEngineUnavailable  = "BROWSER_ENGINE_UNAVAILABLE"
	CodeDecisionEngineUnavailable = "DECISION_ENGINE_UNAVAILABLE"
	CodeNoProviderConfigured      = "NO_PROVIDER_CONFIGURED"
	CodeLiteUnavailable           = "LITE_UNAVAILABLE"
	CodeCommandPathLimited        = "COMMAND_PATH_LIMITED"
)

// ProbeLiteSlot is the wire id for observed lite-slot liveness.
const ProbeLiteSlot = "lite_slot"

// Discriminators select notice variants.
const (
	// ReasonOSBelowFloor: the OS is older than the floor.
	ReasonOSBelowFloor = "below_floor"
	// ReasonOSUnreadable: the version could not be read, which proves nothing.
	ReasonOSUnreadable = "unreadable"

	// ReasonFileLimitProcess: this engine holds every descriptor it is allowed.
	// Reopening the app clears it.
	ReasonFileLimitProcess = "process"
	// ReasonFileLimitSystem: the machine's own file table is full, so reopening
	// this app alone may not be enough.
	ReasonFileLimitSystem = "system"
)

// DecisionReason classifies why the local decision engine cannot answer.
type DecisionReason string

const (
	// ReasonDecisionDisabled: switched off by configuration.
	ReasonDecisionDisabled DecisionReason = "disabled"
	// ReasonDecisionBinaryMissing: the engine executable is not beside the host.
	ReasonDecisionBinaryMissing DecisionReason = "binary_missing"
	// ReasonDecisionModelMissing: the checkpoint is not installed yet, or is still downloading.
	ReasonDecisionModelMissing DecisionReason = "model_missing"
	// ReasonDecisionUnusable: the engine started but did not answer its handshake.
	ReasonDecisionUnusable DecisionReason = "unusable"
)

// BrowserReason classifies the user-actionable browser preflight failures.
type BrowserReason string

const (
	ReasonBrowserBundleMissing       BrowserReason = "bundle_missing"
	ReasonBrowserManagedCacheMissing BrowserReason = "managed_cache_missing"
	ReasonBrowserUnusable            BrowserReason = "unusable"
)

// Result is one probe's verdict. Code is empty when Status is StatusOK.
type Result struct {
	ID     string            `json:"id"`
	Status Status            `json:"status"`
	Code   string            `json:"code,omitempty"`
	Detail map[string]string `json:"detail,omitempty"` // closed, non-sensitive values
}

// Probe is one environment check. ID is stable and wire-visible.
type Probe interface {
	ID() string
	Run(ctx context.Context, env Env) Result
}

// Env provides probe dependencies.
type Env struct {
	// ConfigDir is the device configuration directory.
	ConfigDir       string
	UserPathSource  userpath.Source
	UserPathFailure userpath.Failure

	FreeBytes     func(path string) (uint64, error)
	OSProductVer  func() (string, error)
	ProviderCount func() int
	// MissingRoleProviders maps roles to unavailable provider ids.
	MissingRoleProviders func() map[string]string
	// LiteSlotUnavailable reports observed lite-model failure.
	LiteSlotUnavailable func() (bool, map[string]string)
	// ResolveGitEngine overrides engine resolution in tests.
	ResolveGitEngine func() error
	// ResolveScanner returns the scanner path and failure class.
	ResolveScanner func() (string, string, error)
	// CheckBrowser validates the resolved browser.
	CheckBrowser func(context.Context) (BrowserReason, error)
	// CheckDecisionEngine reports why the decision engine cannot answer, or "" when it can.
	CheckDecisionEngine func(context.Context) DecisionReason
}

// Registry holds the probes in wire order.
type Registry struct{ probes []Probe }

// NewRegistry builds a registry in wire order.
func NewRegistry(probes ...Probe) *Registry {
	return &Registry{probes: probes}
}

// Run executes probes serially in registration order.
func (r *Registry) Run(ctx context.Context, env Env) ([]Result, Status) {
	results := make([]Result, 0, len(r.probes))
	overall := StatusOK

	for _, p := range r.probes {
		started := time.Now()
		res := p.Run(ctx, env)
		res.ID = p.ID()
		observability.LogLatency(
			"preflight_probe",
			"preflight probe completed",
			started,
			"probe_id", res.ID,
			"status", string(res.Status),
		)
		if res.Status == StatusOK {
			// Successful results omit error context.
			res.Code = ""
		}
		results = append(results, res)
		if severity(res.Status) > severity(overall) {
			overall = res.Status
		}
	}

	return results, overall
}

// IDs returns the registered probe ids, in wire order.
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.probes))
	for _, p := range r.probes {
		out = append(out, p.ID())
	}
	return out
}

// Default returns the shipped registry, in wire order.
func Default() *Registry {
	return NewRegistry(
		osVersionProbe{},
		configDirProbe{},
		diskSpaceProbe{},
		commandPathProbe{},
		gitEngineProbe{},
		scannerEngineProbe{},
		browserEngineProbe{},
		decisionEngineProbe{},
		providerConfiguredProbe{},
		liteSlotProbe{},
	)
}
