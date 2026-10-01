package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/spawn"
)

type SessionLimits struct {
	MaxIterations                 int     `yaml:"max_iterations" json:"max_iterations"`
	MaxToolResultBytes            int     `yaml:"max_tool_result_bytes" json:"max_tool_result_bytes"`
	MaxToolSpillBytes             int     `yaml:"max_tool_spill_bytes" json:"max_tool_spill_bytes"`
	SessionSpendCeilingUSD        float64 `yaml:"session_spend_ceiling_usd" json:"session_spend_ceiling_usd"`
	SpendWarningRatio             float64 `yaml:"spend_warning_ratio" json:"spend_warning_ratio"`
	SpendCeilingEnabled           bool    `yaml:"spend_ceiling_enabled" json:"spend_ceiling_enabled"`
	SpendSoftStop                 *bool   `yaml:"spend_soft_stop" json:"spend_soft_stop,omitempty"`
	CoordinatorLoop               *bool   `yaml:"coordinator_loop" json:"coordinator_loop,omitempty"`
	MaxCoordinatorLoopCycles      int     `yaml:"max_coordinator_loop_cycles" json:"max_coordinator_loop_cycles,omitempty"`
	OverlayPromoteMaxIterations   int     `yaml:"overlay_promote_max_iterations" json:"overlay_promote_max_iterations,omitempty"`
	LLMTurnTimeoutSec             int     `yaml:"llm_turn_timeout_sec" json:"llm_turn_timeout_sec"`
	LLMStreamStallTimeoutSec      int     `yaml:"llm_stream_stall_timeout_sec" json:"llm_stream_stall_timeout_sec"`
	CoordinatorHostTurnTimeoutSec int     `yaml:"coordinator_host_turn_timeout_sec" json:"coordinator_host_turn_timeout_sec"`
	CoordinatorMaxSleepSec        int     `yaml:"coordinator_max_sleep_sec" json:"coordinator_max_sleep_sec"`
	AwaitParentWorkersTimeoutSec  int     `yaml:"await_parent_workers_timeout_sec" json:"await_parent_workers_timeout_sec"`
	WorkerToolBudgetDefault       int     `yaml:"worker_tool_budget_default" json:"worker_tool_budget_default"`
	WorkerToolBudgetMin           int     `yaml:"worker_tool_budget_min" json:"worker_tool_budget_min"`
	WorkerToolBudgetMax           int     `yaml:"worker_tool_budget_max" json:"worker_tool_budget_max"`
}

var (
	bundledSessionOnce sync.Once
	bundledSession     SessionLimits
	bundledSessionErr  error
)

// Invalid bundled limits stop startup.
func DefaultSessionLimits() SessionLimits {
	bundledSessionOnce.Do(func() {
		bundledSession, bundledSessionErr = loadBundledSessionLimits()
	})
	if bundledSessionErr != nil {
		panic(bundledSessionErr)
	}
	return bundledSession
}

func loadBundledSessionLimits() (SessionLimits, error) {
	data, err := config.Read(config.SessionLimits)
	if err != nil {
		return SessionLimits{}, fmt.Errorf("read bundled session limits: %w", err)
	}
	var cfg SessionLimits
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return SessionLimits{}, fmt.Errorf("parse bundled session limits: %w", err)
	}
	if cfg.CoordinatorLoop == nil {
		on := true
		cfg.CoordinatorLoop = &on
	}
	// Zero continuous budgets are derived from the live model window.
	budget := spawn.DefaultWorkerToolBudget()
	if cfg.WorkerToolBudgetDefault <= 0 {
		cfg.WorkerToolBudgetDefault = budget.Default
	}
	if cfg.WorkerToolBudgetMin <= 0 {
		cfg.WorkerToolBudgetMin = budget.Min
	}
	if cfg.WorkerToolBudgetMax <= 0 {
		cfg.WorkerToolBudgetMax = budget.Max
	}
	return normalizeWorkerToolBudget(cfg, cfg), nil
}

// LoadSessionLimits reads session settings from path and merges onto bundled defaults.
func LoadSessionLimits(path string) (SessionLimits, error) {
	cfg := DefaultSessionLimits()
	// Missing user settings retain bundled defaults.
	data, err := os.ReadFile(path) // #nosec G304 -- caller-provided settings path
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	var file SessionLimits
	if err := config.DecodeYAML(data, &file); err != nil {
		return cfg, err
	}
	return MergeSessionLimits(cfg, file), nil
}

// MergeSessionLimits overlays non-zero/non-nil fields from overlay onto base.
func MergeSessionLimits(base, overlay SessionLimits) SessionLimits {
	out := base
	if overlay.MaxIterations > 0 {
		out.MaxIterations = overlay.MaxIterations
	}
	if overlay.MaxToolResultBytes > 0 {
		out.MaxToolResultBytes = overlay.MaxToolResultBytes
	}
	if overlay.MaxToolSpillBytes > 0 {
		out.MaxToolSpillBytes = overlay.MaxToolSpillBytes
	}
	// Project overlays can only tighten the device spend ceiling.
	if overlay.SessionSpendCeilingUSD > 0 &&
		(out.SessionSpendCeilingUSD <= 0 || overlay.SessionSpendCeilingUSD < out.SessionSpendCeilingUSD) {
		out.SessionSpendCeilingUSD = overlay.SessionSpendCeilingUSD
	}
	if overlay.SpendCeilingEnabled {
		out.SpendCeilingEnabled = true
	}
	// Project overlays can only disable the device soft stop.
	if overlay.SpendSoftStop != nil && !*overlay.SpendSoftStop {
		off := false
		out.SpendSoftStop = &off
	}
	if overlay.SpendWarningRatio >= 0.1 && overlay.SpendWarningRatio <= 0.95 {
		out.SpendWarningRatio = overlay.SpendWarningRatio
	}
	if overlay.CoordinatorLoop != nil {
		out.CoordinatorLoop = overlay.CoordinatorLoop
	}
	if overlay.MaxCoordinatorLoopCycles > 0 {
		out.MaxCoordinatorLoopCycles = overlay.MaxCoordinatorLoopCycles
	}
	if overlay.OverlayPromoteMaxIterations > 0 {
		out.OverlayPromoteMaxIterations = overlay.OverlayPromoteMaxIterations
	}
	if overlay.LLMTurnTimeoutSec > 0 {
		out.LLMTurnTimeoutSec = overlay.LLMTurnTimeoutSec
	}
	if overlay.LLMStreamStallTimeoutSec > 0 {
		out.LLMStreamStallTimeoutSec = overlay.LLMStreamStallTimeoutSec
	}
	if overlay.CoordinatorHostTurnTimeoutSec > 0 {
		out.CoordinatorHostTurnTimeoutSec = overlay.CoordinatorHostTurnTimeoutSec
	}
	if overlay.CoordinatorMaxSleepSec > 0 {
		out.CoordinatorMaxSleepSec = overlay.CoordinatorMaxSleepSec
	}
	if overlay.AwaitParentWorkersTimeoutSec > 0 {
		out.AwaitParentWorkersTimeoutSec = overlay.AwaitParentWorkersTimeoutSec
	}
	if overlay.WorkerToolBudgetDefault > 0 {
		out.WorkerToolBudgetDefault = overlay.WorkerToolBudgetDefault
	}
	if overlay.WorkerToolBudgetMin > 0 {
		out.WorkerToolBudgetMin = overlay.WorkerToolBudgetMin
	}
	if overlay.WorkerToolBudgetMax > 0 {
		out.WorkerToolBudgetMax = overlay.WorkerToolBudgetMax
	}
	return NormalizeSessionLimits(out)
}

// ProjectSpendOverlay retains only a project's tighter spend guardrail.
func ProjectSpendOverlay(request, global SessionLimits) SessionLimits {
	if !request.SpendCeilingEnabled || request.SessionSpendCeilingUSD <= 0 {
		return SessionLimits{}
	}

	overlay := SessionLimits{}
	globalArmed := global.SpendCeilingEnabled && global.SessionSpendCeilingUSD > 0
	if !globalArmed || request.SessionSpendCeilingUSD < global.SessionSpendCeilingUSD {
		overlay.SessionSpendCeilingUSD = request.SessionSpendCeilingUSD
		overlay.SpendCeilingEnabled = true
	}
	if request.SpendWarningRatio >= 0.1 && request.SpendWarningRatio <= 0.95 &&
		request.SpendWarningRatio != global.SpendWarningRatio {
		overlay.SpendWarningRatio = request.SpendWarningRatio
	}
	if request.SpendSoftStop != nil && !*request.SpendSoftStop && global.SpendSoftStopEnabled() {
		off := false
		overlay.SpendSoftStop = &off
	}
	return overlay
}

// NormalizeSessionLimits fills numeric defaults except continuous budgets derived from true_window.
func NormalizeSessionLimits(lim SessionLimits) SessionLimits {
	def := DefaultSessionLimits()
	if lim.MaxIterations <= 0 {
		lim.MaxIterations = def.MaxIterations
	}
	if lim.MaxToolSpillBytes <= 0 {
		lim.MaxToolSpillBytes = def.MaxToolSpillBytes
	}
	if lim.SessionSpendCeilingUSD < 0 {
		lim.SessionSpendCeilingUSD = 0
	}
	if lim.SpendWarningRatio < 0.1 || lim.SpendWarningRatio > 0.95 {
		lim.SpendWarningRatio = def.SpendWarningRatio
	}
	if lim.SpendSoftStop == nil {
		lim.SpendSoftStop = def.SpendSoftStop
	}
	if lim.LLMTurnTimeoutSec <= 0 {
		lim.LLMTurnTimeoutSec = def.LLMTurnTimeoutSec
	}
	if lim.LLMStreamStallTimeoutSec <= 0 {
		lim.LLMStreamStallTimeoutSec = def.LLMStreamStallTimeoutSec
	}
	if lim.CoordinatorHostTurnTimeoutSec <= 0 {
		lim.CoordinatorHostTurnTimeoutSec = def.CoordinatorHostTurnTimeoutSec
	}
	if lim.CoordinatorMaxSleepSec <= 0 {
		lim.CoordinatorMaxSleepSec = def.CoordinatorMaxSleepSec
	}
	if lim.AwaitParentWorkersTimeoutSec <= 0 {
		lim.AwaitParentWorkersTimeoutSec = def.AwaitParentWorkersTimeoutSec
	}
	if lim.OverlayPromoteMaxIterations <= 0 {
		lim.OverlayPromoteMaxIterations = def.OverlayPromoteMaxIterations
	}
	lim = normalizeWorkerToolBudget(lim, def)
	return lim
}

// normalizeWorkerToolBudget fills zero fields from defaults, then clamps so the
// floor ≤ min ≤ default ≤ max invariant always holds.
func normalizeWorkerToolBudget(lim, def SessionLimits) SessionLimits {
	if lim.WorkerToolBudgetDefault <= 0 {
		lim.WorkerToolBudgetDefault = def.WorkerToolBudgetDefault
	}
	if lim.WorkerToolBudgetMin <= 0 {
		lim.WorkerToolBudgetMin = def.WorkerToolBudgetMin
	}
	if lim.WorkerToolBudgetMax <= 0 {
		lim.WorkerToolBudgetMax = def.WorkerToolBudgetMax
	}
	if lim.WorkerToolBudgetMin < spawn.WorkerToolBudgetFloor {
		lim.WorkerToolBudgetMin = spawn.WorkerToolBudgetFloor
	}
	if lim.WorkerToolBudgetMin > spawn.DefaultWorkerToolBudgetMax {
		lim.WorkerToolBudgetMin = spawn.DefaultWorkerToolBudgetMax
	}
	if lim.WorkerToolBudgetMax > spawn.DefaultWorkerToolBudgetMax {
		lim.WorkerToolBudgetMax = spawn.DefaultWorkerToolBudgetMax
	}
	if lim.WorkerToolBudgetMax < lim.WorkerToolBudgetMin {
		lim.WorkerToolBudgetMax = lim.WorkerToolBudgetMin
	}
	if lim.WorkerToolBudgetDefault < lim.WorkerToolBudgetMin {
		lim.WorkerToolBudgetDefault = lim.WorkerToolBudgetMin
	}
	if lim.WorkerToolBudgetDefault > lim.WorkerToolBudgetMax {
		lim.WorkerToolBudgetDefault = lim.WorkerToolBudgetMax
	}
	return lim
}

// WorkerToolBudget returns the effective worker ceiling bounds.
func (l SessionLimits) WorkerToolBudget() spawn.WorkerToolBudget {
	return spawn.WorkerToolBudget{
		Default: l.WorkerToolBudgetDefault,
		Min:     l.WorkerToolBudgetMin,
		Max:     l.WorkerToolBudgetMax,
	}
}

// CoordinatorLoopEnabled returns whether host auto-Prompt is allowed.
func (l SessionLimits) CoordinatorLoopEnabled() bool {
	if l.CoordinatorLoop == nil {
		return true
	}
	return *l.CoordinatorLoop
}

// SpendSoftStopEnabled reports whether crossing a ceiling during a running
// coordinator turn receives one bounded tool-capable landing round.
func (l SessionLimits) SpendSoftStopEnabled() bool {
	if l.SpendSoftStop == nil {
		return true
	}
	return *l.SpendSoftStop
}

// LLMTurnTimeout returns the per-completion stream wall clock for one LLM turn.
func (l SessionLimits) LLMTurnTimeout() time.Duration {
	return sessionWallClockTimeout(l.LLMTurnTimeoutSec, DefaultSessionLimits().LLMTurnTimeoutSec)
}

// Stream stalls and the total turn deadline have separate limits.
func (l SessionLimits) LLMStreamStallTimeout() time.Duration {
	return sessionWallClockTimeout(l.LLMStreamStallTimeoutSec, DefaultSessionLimits().LLMStreamStallTimeoutSec)
}

// CoordinatorHostTurnTimeout returns the wall clock for one host-initiated coordinator prompt turn.
func (l SessionLimits) CoordinatorHostTurnTimeout() time.Duration {
	return sessionWallClockTimeout(l.CoordinatorHostTurnTimeoutSec, DefaultSessionLimits().CoordinatorHostTurnTimeoutSec)
}

// CoordinatorMaxSleep returns the maximum wait() sleep duration for the coordinator loop.
func (l SessionLimits) CoordinatorMaxSleep() time.Duration {
	return sessionWallClockTimeout(l.CoordinatorMaxSleepSec, DefaultSessionLimits().CoordinatorMaxSleepSec)
}

// AwaitParentWorkersTimeout returns how long to block waiting for parent-session workers.
func (l SessionLimits) AwaitParentWorkersTimeout() time.Duration {
	return sessionWallClockTimeout(l.AwaitParentWorkersTimeoutSec, DefaultSessionLimits().AwaitParentWorkersTimeoutSec)
}

func sessionWallClockTimeout(sec, defaultSec int) time.Duration {
	if sec <= 0 {
		sec = defaultSec
	}
	return time.Duration(sec) * time.Second
}

// ApplyDerivedSessionLimits fills zero continuous-budget fields from
// ScaleLimitsFromTrueWindow. Non-zero overlay values win.
func ApplyDerivedSessionLimits(lim SessionLimits, derived llm.SessionLimitFields) SessionLimits {
	if lim.MaxToolResultBytes <= 0 {
		lim.MaxToolResultBytes = derived.MaxToolResultBytes
	}
	if lim.MaxCoordinatorLoopCycles <= 0 {
		lim.MaxCoordinatorLoopCycles = derived.MaxCoordinatorLoopCycles
	}
	return lim
}

// EffectiveMaxCoordinatorLoopCycles returns the per-run loop wake budget.
func (l SessionLimits) EffectiveMaxCoordinatorLoopCycles() int {
	if l.MaxCoordinatorLoopCycles <= 0 {
		return llm.ScaleLimitsFromTrueWindow(modelinfo.DefaultFallbackTrueWindow).MaxCoordinatorLoopCycles
	}
	return l.MaxCoordinatorLoopCycles
}

// SettingsFingerprint hashes session limit fields that invalidate compiled prompt cache.
func (l SessionLimits) SettingsFingerprint() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"iter=%d|tool=%d|timeout=%d|stall=%d|host=%d|sleep=%d|workers=%d|budget=%d/%d/%d",
		l.MaxIterations,
		l.MaxToolResultBytes,
		l.LLMTurnTimeoutSec,
		l.LLMStreamStallTimeoutSec,
		l.CoordinatorHostTurnTimeoutSec,
		l.CoordinatorMaxSleepSec,
		l.AwaitParentWorkersTimeoutSec,
		l.WorkerToolBudgetDefault,
		l.WorkerToolBudgetMin,
		l.WorkerToolBudgetMax,
	)))
	return hex.EncodeToString(sum[:8])
}
