package kick_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func configRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// The task-finished notice renders every result shape with and without the
// tools it names: supplied values appear, and a tool is named only when offered.
func TestWorkerResultNoticeRendersEveryShape(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configRoot(t)})
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	named := []string{"promote_overlay", "reject_overlay", "instruct_worker"}
	namedLoaded := map[string]bool{}
	for _, tool := range named {
		namedLoaded[tool] = true
	}
	withNamed, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		Profiles:  profiles,
		Visible:   named,
		Loaded:    namedLoaded,
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader withNamed", err)
	withoutNamed, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		Profiles:  profiles,
		Visible:   []string{"recall"},
		Loaded:    map[string]bool{"recall": true},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader withoutNamed", err)
	shapes := map[string]map[string]any{
		"changed": {"worker": "fixer", "leg_status": "complete", "result_handle": "result-1",
			"changed": []string{"src/a.go"}, "changed_omitted": 2, "not_applied": []string(nil)},
		"no changes": {"worker": "fixer", "leg_status": "complete", "result_handle": "result-2",
			"changed": []string(nil), "not_applied": []string(nil)},
		"missed instruction": {"worker": "fixer", "leg_status": "partial", "result_handle": "result-3",
			"changed": []string{"src/b.go"}, "not_applied": []string{"queued note"}},
		"no result": {"worker": "fixer", "leg_status": "failed", "changed": []string(nil), "not_applied": []string(nil)},
	}
	for name, notice := range shapes {
		for _, offered := range []bool{true, false} {
			reader := withoutNamed
			if offered {
				reader = withNamed
			}
			data := map[string]any{"worker_result": notice}
			prompts.MergeCoordinatorKickVarsForTools(data, reader)
			out, err := engine.RenderKick(t.Context(), "coordinator-worker-task-finished", data)
			testutil.FailErr(t, name, err)
			for _, want := range []string{"`fixer`", notice["leg_status"].(string)} {
				if !strings.Contains(out, want) {
					t.Fatalf("%s offered=%v: missing %q\n%s", name, offered, want, out)
				}
			}
			if h, ok := notice["result_handle"].(string); ok && !strings.Contains(out, "`"+h+"`") {
				t.Fatalf("%s: missing handle %q\n%s", name, h, out)
			}
			for _, p := range notice["changed"].([]string) {
				if !strings.Contains(out, "`"+p+"`") {
					t.Fatalf("%s: missing changed path %q\n%s", name, p, out)
				}
			}
			for _, m := range notice["not_applied"].([]string) {
				if !strings.Contains(out, m) {
					t.Fatalf("%s: missing instruction %q\n%s", name, m, out)
				}
			}
			if !offered {
				for _, tool := range named {
					if strings.Contains(out, "`"+tool+"`") {
						t.Fatalf("%s: names %s without offering it\n%s", name, tool, out)
					}
				}
			}
		}
	}
}
