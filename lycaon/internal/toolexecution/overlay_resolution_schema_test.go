package toolexecution

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/argdiag"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestOverlayResolutionSchemaRejectsUnsupportedNestedFields(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load tool schemas", err)
	meta, ok := cfg.ToolMeta("promote_overlay")
	if !ok {
		t.Fatal("promote_overlay schema missing")
	}
	bp := stockBlockPlane(t)
	for _, tc := range []struct {
		name       string
		resolution map[string]any
		want       string
		issuePath  []string
	}{
		{"nested resolutions", map[string]any{"path": "status.py", "resolutions": []any{map[string]any{"action": "keep_theirs"}}}, "resolutions", []string{"resolutions", "0"}},
		{"unknown resolution field", map[string]any{"path": "status.py", "action": "keep_theirs", "unexpected": true}, "unexpected", []string{"resolutions", "0"}},
		{"unknown hunk field", map[string]any{"path": "status.py", "hunks": []any{map[string]any{"start_line": 1, "content": "fixed", "unexpected": true}}}, "unexpected", []string{"resolutions", "0", "hunks", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			reg := tools.NewDefaultRegistry()
			registerTestDefinition(t, reg, "promote_overlay", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				called = true
				return "ok", nil
			})
			def, _ := reg.Definition("promote_overlay")
			def.Meta = tools.ToolMeta{Name: "promote_overlay", ArgsSchema: meta.ArgsSchema}
			testutil.FailErr(t, "register schema", reg.RegisterDefinition(def))
			executor := NewExecutor(nil, reg, "implement")
			executor.Rejections.SetBlockPlane(bp)
			args := map[string]any{"overlay_id": "worker-1", "resolutions": []any{tc.resolution}}
			_, err := executor.Invoke(t.Context(), "promote_overlay", args, tools.ToolContext{})
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != "TOOL_ARGS_INVALID" || called {
				t.Fatalf("invalid nested object reached wrong boundary: called=%v reject=%+v error=%v", called, reject, err)
			}
			issues, ok := reject.Data["schema_issues"].([]argdiag.SchemaIssue)
			if !ok || len(issues) != 1 || !reflect.DeepEqual(issues[0].Path, tc.issuePath) || !reflect.DeepEqual(issues[0].Keyword, []string{"additionalProperties"}) {
				t.Fatalf("missing typed schema issue: %+v", reject.Data)
			}
			for _, want := range []string{"TOOL_ARGS_INVALID", tc.want, "resolutions", "Fix:"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("rendered rejection missing %q: %v", want, err)
				}
			}
			t.Log(err.Error())
		})
	}
	for _, resolution := range []map[string]any{
		{"path": "status.py", "action": "keep_theirs"},
		{"path": "status.py", "action": "keep_ours"},
		{"path": "status.py", "action": "drop"},
		{"path": "status.py", "action": "keep_both", "content": "merged"},
		{"path": "status.py", "content": "merged"},
		{"path": "status.py", "hunks": []any{map[string]any{"start_line": 1, "content": "merged"}}},
		{"path": "status.py", "hunks": []any{map[string]any{"start_line": 1, "end_line": 2, "content": "merged"}}},
	} {
		testutil.FailErr(t, "valid resolution", tools.ValidateToolArgs(meta.ArgsSchema, map[string]any{"overlay_id": "worker-1", "resolutions": []any{resolution}}))
	}
}

func TestOverlayResolutionPublishedObjectsStayClosed(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load tool schemas", err)
	meta, ok := cfg.ToolMeta("promote_overlay")
	if !ok {
		t.Fatal("promote_overlay schema missing")
	}
	projected := tools.TrimCoordinatorToolMeta(tools.ToolMeta{Name: meta.Name, ArgsSchema: meta.ArgsSchema})
	for name, schema := range map[string]map[string]any{"canonical": meta.ArgsSchema, "provider": projected.ArgsSchema} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"overlay_id": "worker-1", "resolutions": []any{map[string]any{"path": "status.py", "hunks": []any{map[string]any{"start_line": 1, "content": "merged"}}}}}
			testutil.FailErr(t, "valid call", tools.ValidateToolArgs(schema, args))
			visited := 0
			var visit func(any)
			visit = func(value any) {
				switch value := value.(type) {
				case map[string]any:
					visited++
					value["unsupported_field"] = true
					if tools.ValidateToolArgs(schema, args) == nil {
						t.Errorf("published object %d silently accepts an unsupported field", visited)
					}
					delete(value, "unsupported_field")
					for _, child := range value {
						visit(child)
					}
				case []any:
					for _, child := range value {
						visit(child)
					}
				}
			}
			visit(args)
			if visited != 3 {
				t.Fatalf("covered %d object boundaries, want root, resolution, and hunk", visited)
			}
		})
	}
}
