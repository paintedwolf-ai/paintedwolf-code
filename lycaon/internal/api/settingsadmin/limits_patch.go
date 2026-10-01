package settingsadmin

import (
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func validateLimitsPatch(raw map[string]any, req wire.SettingsLimitsPatch) (string, string) {
	if _, ok := raw["max_iterations"]; ok && raw["max_iterations"] != nil {
		if *req.MaxIterations < 1 {
			return "max_iterations", "max_iterations must be >= 1"
		}
	}
	if _, ok := raw["max_tool_result_bytes"]; ok && raw["max_tool_result_bytes"] != nil {
		if *req.MaxToolResultBytes < 0 {
			return "max_tool_result_bytes", "max_tool_result_bytes must be >= 0"
		}
	}
	if _, ok := raw["max_coordinator_loop_cycles"]; ok && raw["max_coordinator_loop_cycles"] != nil {
		if *req.MaxCoordinatorLoopCycles < 0 {
			return "max_coordinator_loop_cycles", "max_coordinator_loop_cycles must be >= 0"
		}
	}
	if _, ok := raw["overlay_promote_max_iterations"]; ok && raw["overlay_promote_max_iterations"] != nil {
		if *req.OverlayPromoteMaxIterations < 1 {
			return "overlay_promote_max_iterations", "overlay_promote_max_iterations must be >= 1"
		}
	}
	if _, ok := raw["llm_turn_timeout_ms"]; ok && raw["llm_turn_timeout_ms"] != nil {
		if msg := wholeSecondsMs(*req.LLMTurnTimeoutMs); msg != "" {
			return "llm_turn_timeout_ms", "llm_turn_timeout_ms " + msg
		}
	}
	if _, ok := raw["coordinator_host_turn_timeout_ms"]; ok && raw["coordinator_host_turn_timeout_ms"] != nil {
		if msg := wholeSecondsMs(*req.CoordinatorHostTurnTimeoutMs); msg != "" {
			return "coordinator_host_turn_timeout_ms", "coordinator_host_turn_timeout_ms " + msg
		}
	}
	if _, ok := raw["coordinator_max_sleep_ms"]; ok && raw["coordinator_max_sleep_ms"] != nil {
		if msg := wholeSecondsMs(*req.CoordinatorMaxSleepMs); msg != "" {
			return "coordinator_max_sleep_ms", "coordinator_max_sleep_ms " + msg
		}
	}
	if _, ok := raw["await_parent_workers_timeout_ms"]; ok && raw["await_parent_workers_timeout_ms"] != nil {
		if msg := wholeSecondsMs(*req.AwaitParentWorkersTimeoutMs); msg != "" {
			return "await_parent_workers_timeout_ms", "await_parent_workers_timeout_ms " + msg
		}
	}
	if _, ok := raw["worker_tool_budget_default"]; ok && raw["worker_tool_budget_default"] != nil {
		if *req.WorkerToolBudgetDefault < 2 || *req.WorkerToolBudgetDefault > 120 {
			return "worker_tool_budget_default", "worker_tool_budget_default must be between 2 and 120"
		}
	}
	if _, ok := raw["worker_tool_budget_min"]; ok && raw["worker_tool_budget_min"] != nil {
		if *req.WorkerToolBudgetMin < 2 || *req.WorkerToolBudgetMin > 120 {
			return "worker_tool_budget_min", "worker_tool_budget_min must be between 2 and 120"
		}
	}
	if _, ok := raw["worker_tool_budget_max"]; ok && raw["worker_tool_budget_max"] != nil {
		if *req.WorkerToolBudgetMax < 2 || *req.WorkerToolBudgetMax > 120 {
			return "worker_tool_budget_max", "worker_tool_budget_max must be between 2 and 120"
		}
	}
	if _, ok := raw["session_spend_ceiling_nano_usd"]; ok && raw["session_spend_ceiling_nano_usd"] != nil {
		if *req.SessionSpendCeilingNanoUSD < 0 {
			return "session_spend_ceiling_nano_usd", "session_spend_ceiling_nano_usd must be >= 0"
		}
	}
	if _, ok := raw["spend_warning_ratio"]; ok && raw["spend_warning_ratio"] != nil {
		if *req.SpendWarningRatio < 0.1 || *req.SpendWarningRatio > 0.95 {
			return "spend_warning_ratio", "spend_warning_ratio must be between 0.1 and 0.95"
		}
	}
	return "", ""
}

// wholeSecondsMs refuses a duration the store cannot keep: limits persist in
// whole seconds, so a sub-second remainder would be dropped.
func wholeSecondsMs(ms int) string {
	if ms < 1000 || ms%1000 != 0 {
		return "must be a positive whole number of seconds in milliseconds"
	}
	return ""
}

func applyLimitsPatch(lim settings.SessionLimits, base settings.SessionLimits, raw map[string]any, req wire.SettingsLimitsPatch) settings.SessionLimits {
	if _, ok := raw["max_iterations"]; ok {
		if raw["max_iterations"] == nil {
			lim.MaxIterations = base.MaxIterations
		} else {
			lim.MaxIterations = *req.MaxIterations
		}
	}
	if _, ok := raw["max_tool_result_bytes"]; ok {
		if raw["max_tool_result_bytes"] == nil {
			lim.MaxToolResultBytes = base.MaxToolResultBytes
		} else {
			lim.MaxToolResultBytes = *req.MaxToolResultBytes
		}
	}
	if _, ok := raw["overlay_promote_max_iterations"]; ok {
		if raw["overlay_promote_max_iterations"] == nil {
			lim.OverlayPromoteMaxIterations = base.OverlayPromoteMaxIterations
		} else {
			lim.OverlayPromoteMaxIterations = *req.OverlayPromoteMaxIterations
		}
	}
	if _, ok := raw["coordinator_loop"]; ok {
		if raw["coordinator_loop"] == nil {
			lim.CoordinatorLoop = base.CoordinatorLoop
		} else {
			lim.CoordinatorLoop = req.CoordinatorLoop
		}
	}
	if _, ok := raw["max_coordinator_loop_cycles"]; ok {
		if raw["max_coordinator_loop_cycles"] == nil {
			lim.MaxCoordinatorLoopCycles = base.MaxCoordinatorLoopCycles
		} else {
			lim.MaxCoordinatorLoopCycles = *req.MaxCoordinatorLoopCycles
		}
	}
	if _, ok := raw["llm_turn_timeout_ms"]; ok {
		if raw["llm_turn_timeout_ms"] == nil {
			lim.LLMTurnTimeoutSec = base.LLMTurnTimeoutSec
		} else {
			lim.LLMTurnTimeoutSec = *req.LLMTurnTimeoutMs / 1000
		}
	}
	if _, ok := raw["coordinator_host_turn_timeout_ms"]; ok {
		if raw["coordinator_host_turn_timeout_ms"] == nil {
			lim.CoordinatorHostTurnTimeoutSec = base.CoordinatorHostTurnTimeoutSec
		} else {
			lim.CoordinatorHostTurnTimeoutSec = *req.CoordinatorHostTurnTimeoutMs / 1000
		}
	}
	if _, ok := raw["coordinator_max_sleep_ms"]; ok {
		if raw["coordinator_max_sleep_ms"] == nil {
			lim.CoordinatorMaxSleepSec = base.CoordinatorMaxSleepSec
		} else {
			lim.CoordinatorMaxSleepSec = *req.CoordinatorMaxSleepMs / 1000
		}
	}
	if _, ok := raw["await_parent_workers_timeout_ms"]; ok {
		if raw["await_parent_workers_timeout_ms"] == nil {
			lim.AwaitParentWorkersTimeoutSec = base.AwaitParentWorkersTimeoutSec
		} else {
			lim.AwaitParentWorkersTimeoutSec = *req.AwaitParentWorkersTimeoutMs / 1000
		}
	}
	if _, ok := raw["worker_tool_budget_default"]; ok {
		if raw["worker_tool_budget_default"] == nil {
			lim.WorkerToolBudgetDefault = base.WorkerToolBudgetDefault
		} else {
			lim.WorkerToolBudgetDefault = *req.WorkerToolBudgetDefault
		}
	}
	if _, ok := raw["worker_tool_budget_min"]; ok {
		if raw["worker_tool_budget_min"] == nil {
			lim.WorkerToolBudgetMin = base.WorkerToolBudgetMin
		} else {
			lim.WorkerToolBudgetMin = *req.WorkerToolBudgetMin
		}
	}
	if _, ok := raw["worker_tool_budget_max"]; ok {
		if raw["worker_tool_budget_max"] == nil {
			lim.WorkerToolBudgetMax = base.WorkerToolBudgetMax
		} else {
			lim.WorkerToolBudgetMax = *req.WorkerToolBudgetMax
		}
	}
	if _, ok := raw["session_spend_ceiling_nano_usd"]; ok {
		if raw["session_spend_ceiling_nano_usd"] == nil {
			lim.SessionSpendCeilingUSD = base.SessionSpendCeilingUSD
		} else {
			lim.SessionSpendCeilingUSD = cost.NanoToUSD(*req.SessionSpendCeilingNanoUSD)
		}
	}
	if _, ok := raw["spend_warning_ratio"]; ok {
		if raw["spend_warning_ratio"] == nil {
			lim.SpendWarningRatio = base.SpendWarningRatio
		} else {
			lim.SpendWarningRatio = *req.SpendWarningRatio
		}
	}
	if _, ok := raw["spend_ceiling_enabled"]; ok {
		if raw["spend_ceiling_enabled"] == nil {
			lim.SpendCeilingEnabled = base.SpendCeilingEnabled
		} else {
			lim.SpendCeilingEnabled = *req.SpendCeilingEnabled
		}
	}
	if _, ok := raw["spend_soft_stop"]; ok {
		if raw["spend_soft_stop"] == nil {
			lim.SpendSoftStop = base.SpendSoftStop
		} else {
			lim.SpendSoftStop = req.SpendSoftStop
		}
	}
	return lim
}
