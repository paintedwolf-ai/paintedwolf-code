package contract

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/actionenvelope"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestSchemaRejectsMissingRequiredFields(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir failed", err)
	exec := toolfixture.ContractToolExecutor(t)
	exec.Metadata.SetToolSchemas(schemas)

	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{tool: "task", args: map[string]any{}, want: "TOOL_ARGS_INVALID"},
		{tool: "plan_from_blueprint", args: map[string]any{}, want: "TOOL_ARGS_INVALID"},
	}
	for _, tc := range cases {
		_, err := exec.Invoke(context.Background(), tc.tool, tc.args, tools.ToolContext{
			Identity: tools.InvocationIdentity{Agent: "coordinator"},
			Turn: tools.InvocationTurn{
				TurnOfferedToolNames: []string{"write", "edit", "code_rewrite"},
				TurnToolPlan:         toolsurface.Compile([]string{"write", "edit", "code_rewrite"}, nil),
			},
		})
		if err == nil {
			t.Fatalf("%s: expected schema reject", tc.tool)
		}
		msg := err.Error()
		assertStructuredRejectCode(t, err, "TOOL_ARGS_INVALID")
		if !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: error = %q want %q", tc.tool, msg, tc.want)
		}
	}
}

func TestRetiredEditSchemaRejectCarriesCommandOperationCode(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir failed", err)
	exec := toolfixture.ContractToolExecutor(t)
	exec.Metadata.SetToolSchemas(schemas)

	_, err = exec.Invoke(context.Background(), "edit", map[string]any{"path": "ntp_check.py", "symbol": "query_server"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "implementer"},
		Turn: tools.InvocationTurn{
			TurnOfferedToolNames: []string{"command"},
			TurnToolPlan:         toolsurface.Compile([]string{"command"}, nil),
		},
	})
	if err == nil {
		t.Fatal("expected edit schema reject")
	}
	assertStructuredRejectCode(t, err, "TOOL_COMMAND_OPERATION")
}

// A fence named for a tool that carries that tool's own arguments is an action
// when the request offers the tool or can reach it, for every shipped schema.
func TestToolNamedFenceRecognitionTracksWholeToolCatalog(t *testing.T) {
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(contractcheck.RepoRoot(t), "lycaon/config/packs/painted-wolf/platform/tools/schemas"))
	contractcheck.FailErr(t, "load schemas", err)
	if len(schemas.Tools) == 0 {
		t.Fatal("empty tool catalog")
	}
	other := []actionenvelope.Tool{{Name: "not-offered"}}
	for name, entry := range schemas.Tools {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"argument": "value"}
			if properties, _ := entry.Schema["properties"].(map[string]any); len(properties) > 0 {
				args = map[string]any{}
				for property := range properties {
					args[property] = "value"
				}
			}
			raw, err := json.Marshal(args)
			contractcheck.FailErr(t, "encode arguments", err)
			for _, fence := range []string{"```", "~~~"} {
				text := "I will do that now.\n\n" + fence + name + "\n" + string(raw) + "\n" + fence
				want := actionenvelope.Observation{Tools: []string{name}, Fenced: true}
				if got := actionenvelope.Detect(text, []actionenvelope.Tool{{Name: name, ArgsSchema: entry.Schema}}, nil); !slices.Equal(got.Tools, want.Tools) || !got.Fenced {
					t.Fatalf("offered fence not recognized: %s => %+v", text, got)
				}
				if got := actionenvelope.Detect(text, other, []string{name}); !slices.Equal(got.Tools, want.Tools) || !got.Fenced {
					t.Fatalf("reachable fence not recognized: %s => %+v", text, got)
				}
				if got := actionenvelope.Detect(text, other, nil); len(got.Tools) != 0 || got.Fenced {
					t.Fatalf("unreachable fence recognized: %s => %+v", text, got)
				}
				example := fence + "json\n" + string(raw) + "\n" + fence
				if got := actionenvelope.Detect(example, []actionenvelope.Tool{{Name: name, ArgsSchema: entry.Schema}}, nil); len(got.Tools) != 0 {
					t.Fatalf("neutral example became action: %s => %+v", example, got)
				}
			}
		})
	}
}

// Recognition depends on the frozen offer, never a tool-specific allowlist or
// argument contents. Exercise every shipped schema, including future additions.
func TestTextActionRecognitionTracksWholeToolCatalog(t *testing.T) {
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(contractcheck.RepoRoot(t), "lycaon/config/packs/painted-wolf/platform/tools/schemas"))
	contractcheck.FailErr(t, "load schemas", err)
	if len(schemas.Tools) == 0 {
		t.Fatal("empty tool catalog")
	}
	for name := range schemas.Tools {
		t.Run(name, func(t *testing.T) {
			for _, keys := range [][2]string{{"tool_name", "tool_args"}, {"name", "arguments"}, {"name", "parameters"}, {"name", "args"}, {"tool", "args"}} {
				for _, args := range []any{nil, "malformed", map[string]any{"nested": map[string]any{"tool": "not-offered", "args": nil}}} {
					action := map[string]any{keys[0]: name, keys[1]: args}
					for _, envelope := range []any{action, []any{action, action}, map[string]any{"tool_calls": []any{map[string]any{"function": action}}}} {
						raw, err := json.Marshal(envelope)
						contractcheck.FailErr(t, "encode envelope", err)
						text := string(raw)
						if got := actionenvelope.Detect(text, []actionenvelope.Tool{{Name: name}}, nil); !slices.Equal(got.Tools, []string{name}) || got.Fenced {
							t.Fatalf("catalog action not recognized exactly once: %s => %+v", text, got)
						}
						for _, offered := range [][]actionenvelope.Tool{nil, {}, {{Name: "not-offered"}}} {
							if got := actionenvelope.Detect(text, offered, nil); len(got.Tools) != 0 {
								t.Fatalf("unoffered action recognized: %+v", got)
							}
						}
						for _, example := range []string{"Example: " + text, "```json\n" + text + "\n```", `{"data":` + text + `}`, `{"tool":"not-offered","args":` + text + `}`} {
							if got := actionenvelope.Detect(example, []actionenvelope.Tool{{Name: name}}, nil); len(got.Tools) != 0 {
								t.Fatalf("example or argument data became action: %s => %+v", example, got)
							}
						}
					}
				}
			}
		})
	}
}
