package kick_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Kick copy about workers renders with and without worker names and with and
// without the tools it names: supplied names and counts appear, a tool is named
// only when offered.
func TestWorkerKickCopyRendersEveryState(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configRoot(t)})
	named := []string{"preview_overlay", "instruct_worker", "pack_board", "wait"}
	cases := []struct {
		kick string
		data map[string]any
		want []string
	}{
		{"coordinator-scheduled", map[string]any{"pending_overlays": 2, "batch_phase": "open"}, []string{"2"}},
		{"coordinator-scheduled", map[string]any{"batch_phase": "open"}, nil},
		{"coordinator-worker-task-finished", map[string]any{"pending_overlays": 1}, []string{"1"}},
		{"coordinator-worker-task-finished", map[string]any{"partial_workers": 3}, []string{"3"}},
		{"coordinator-worker-round-notice", map[string]any{"notice_kind": "consumption", "worker": "parser-fix", "agent_type": "implementer", "model_calls": 7, "tokens": 0}, []string{"`parser-fix`", "7"}},
		{"coordinator-worker-round-notice", map[string]any{"notice_kind": "quiet", "agent_type": "implementer", "quiet_minutes": 12, "model_calls": 1, "tokens": 0}, []string{"12"}},
		{"coordinator-root-detach-canceled", map[string]any{"path": "/r", "workers": []map[string]any{{"worker": "parser-fix", "agent_type": "implementer", "reason": "root detached"}}, "overlays": []map[string]any{{}}}, []string{"`parser-fix`", "root detached"}},
		{"coordinator-root-detach-canceled", map[string]any{"path": "/r", "workers": []map[string]any{{"agent_type": "implementer", "reason": "root detached"}}}, []string{"implementer", "root detached"}},
	}
	for _, tc := range cases {
		for _, offered := range []bool{true, false} {
			reachable := []string{}
			if offered {
				reachable = named
			}
			data := map[string]any{}
			for k, v := range tc.data {
				data[k] = v
			}
			testutil.FailErr(t, "merge tool vars", prompts.MergeCoordinatorKickVarsForTools(data, reachable))
			out, err := engine.RenderKick(t.Context(), tc.kick, data)
			testutil.FailErr(t, tc.kick, err)
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("%s offered=%v: missing %q\n%s", tc.kick, offered, want, out)
				}
			}
			if !offered {
				for _, tool := range named {
					if strings.Contains(out, "`"+tool+"`") || strings.Contains(out, "`"+tool+"(") {
						t.Fatalf("%s names %s without offering it\n%s", tc.kick, tool, out)
					}
				}
			}
		}
	}
}
