package settingsadmin

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestValidateLimitsPatch(t *testing.T) {
	cases := []struct {
		name      string
		raw       map[string]any
		in        wire.SettingsLimitsPatch
		wantField string
		wantMsg   string
	}{
		{
			name: "valid",
			raw: map[string]any{
				"max_iterations":                   5,
				"overlay_promote_max_iterations":   30,
				"max_tool_result_bytes":            1024,
				"max_coordinator_loop_cycles":      4,
				"llm_turn_timeout_ms":              3600000,
				"coordinator_host_turn_timeout_ms": 3600000,
				"coordinator_max_sleep_ms":         3600000,
				"await_parent_workers_timeout_ms":  3600000,
			},
			in: wire.SettingsLimitsPatch{
				MaxIterations:                new(5),
				OverlayPromoteMaxIterations:  new(30),
				MaxToolResultBytes:           new(1024),
				MaxCoordinatorLoopCycles:     new(4),
				LLMTurnTimeoutMs:             new(3600000),
				CoordinatorHostTurnTimeoutMs: new(3600000),
				CoordinatorMaxSleepMs:        new(3600000),
				AwaitParentWorkersTimeoutMs:  new(3600000),
			},
		},
		{
			name:      "max_iterations zero",
			raw:       map[string]any{"max_iterations": 0},
			in:        wire.SettingsLimitsPatch{MaxIterations: new(0)},
			wantField: "max_iterations",
			wantMsg:   "max_iterations must be >= 1",
		},
		{
			name:      "max_iterations negative",
			raw:       map[string]any{"max_iterations": -1},
			in:        wire.SettingsLimitsPatch{MaxIterations: new(-1)},
			wantField: "max_iterations",
			wantMsg:   "max_iterations must be >= 1",
		},
		{
			name:      "max_tool_result_bytes negative",
			raw:       map[string]any{"max_tool_result_bytes": -1},
			in:        wire.SettingsLimitsPatch{MaxToolResultBytes: new(-1)},
			wantField: "max_tool_result_bytes",
			wantMsg:   "max_tool_result_bytes must be >= 0",
		},
		{
			name:      "max_coordinator_loop_cycles negative",
			raw:       map[string]any{"max_coordinator_loop_cycles": -1},
			in:        wire.SettingsLimitsPatch{MaxCoordinatorLoopCycles: new(-1)},
			wantField: "max_coordinator_loop_cycles",
			wantMsg:   "max_coordinator_loop_cycles must be >= 0",
		},
		{
			name:      "sub-second timeout",
			raw:       map[string]any{"llm_turn_timeout_ms": 1500},
			in:        wire.SettingsLimitsPatch{LLMTurnTimeoutMs: new(1500)},
			wantField: "llm_turn_timeout_ms",
			wantMsg:   "llm_turn_timeout_ms must be a positive whole number of seconds in milliseconds",
		},
		{
			name:      "zero timeout",
			raw:       map[string]any{"coordinator_max_sleep_ms": 0},
			in:        wire.SettingsLimitsPatch{CoordinatorMaxSleepMs: new(0)},
			wantField: "coordinator_max_sleep_ms",
			wantMsg:   "coordinator_max_sleep_ms must be a positive whole number of seconds in milliseconds",
		},
		{
			name:      "warning ratio below range",
			raw:       map[string]any{"spend_warning_ratio": 0.09},
			in:        wire.SettingsLimitsPatch{SpendWarningRatio: new(0.09)},
			wantField: "spend_warning_ratio",
			wantMsg:   "spend_warning_ratio must be between 0.1 and 0.95",
		},
		{
			name:      "warning ratio above range",
			raw:       map[string]any{"spend_warning_ratio": 0.96},
			in:        wire.SettingsLimitsPatch{SpendWarningRatio: new(0.96)},
			wantField: "spend_warning_ratio",
			wantMsg:   "spend_warning_ratio must be between 0.1 and 0.95",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotField, gotMsg := validateLimitsPatch(tc.raw, tc.in)
			if gotField != tc.wantField || gotMsg != tc.wantMsg {
				t.Errorf("validateLimitsPatch(%+v, %+v) = (%q, %q), want (%q, %q)", tc.raw, tc.in, gotField, gotMsg, tc.wantField, tc.wantMsg)
			}
		})
	}
}

func TestLimitsPatchPreservesOmissionsAndResetsToInheritedValues(t *testing.T) {
	current := settings.SessionLimits{MaxIterations: 9, MaxCoordinatorLoopCycles: 6, WorkerToolBudgetDefault: 12}
	inherited := settings.SessionLimits{MaxIterations: 17, MaxCoordinatorLoopCycles: 3, WorkerToolBudgetDefault: 24}
	body := []byte(`{"max_iterations":null,"max_coordinator_loop_cycles":0}`)
	var raw map[string]any
	var patch wire.SettingsLimitsPatch
	testutil.FailErr(t, "decode patch presence", json.Unmarshal(body, &raw))
	testutil.FailErr(t, "decode patch values", json.Unmarshal(body, &patch))
	field, message := validateLimitsPatch(raw, patch)
	if field != "" || message != "" {
		t.Fatalf("valid reset and zero patch rejected: %s %s", field, message)
	}
	got := applyLimitsPatch(current, inherited, raw, patch)
	if got.MaxIterations != inherited.MaxIterations || got.MaxCoordinatorLoopCycles != 0 || got.WorkerToolBudgetDefault != current.WorkerToolBudgetDefault {
		t.Fatalf("merged limits = %+v", got)
	}
}
