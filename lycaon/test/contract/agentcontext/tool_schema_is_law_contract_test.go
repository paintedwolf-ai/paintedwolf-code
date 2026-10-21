package contract

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
		{tool: "read", args: map[string]any{"filepath": "x.go"}, want: "received keys: filepath"},
		{tool: "edit", args: map[string]any{"path": "ntp_check.py", "symbol": "query_server"}, want: "TOOL_ARGS_INVALID"},
		{tool: "code_rewrite", args: map[string]any{"pattern": "a", "rewrite": "b"}, want: "TOOL_ARGS_INVALID"},
		{tool: "delegate_init", args: map[string]any{"blueprint_path": "p1"}, want: "TOOL_ARGS_INVALID"},
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

func TestEditSchemaRejectPreservesSourcePrerequisite(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir failed", err)
	exec := toolfixture.ContractToolExecutor(t)
	exec.Metadata.SetToolSchemas(schemas)

	_, err = exec.Invoke(context.Background(), "edit", map[string]any{"path": "ntp_check.py", "symbol": "query_server"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "implementer"},
		Turn: tools.InvocationTurn{
			TurnOfferedToolNames: []string{"write", "edit"},
			TurnToolPlan:         toolsurface.Compile([]string{"write", "edit"}, nil),
		},
	})
	if err == nil {
		t.Fatal("expected edit schema reject")
	}
	if !strings.Contains(err.Error(), "read the current source") || strings.Contains(err.Error(), "switch to `write`") {
		t.Fatalf("reject should preserve edit source prerequisite without switching to write, got %v", err)
	}
}

func TestNativeOwnersDoNotFmtMissingArg(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "tools", "native")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Errorf" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "fmt" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			msg := strings.Trim(lit.Value, "`\"")
			if strings.HasPrefix(msg, "missing ") || strings.HasPrefix(msg, "missing'") {
				t.Errorf("%s: handler fmt.Errorf missing-arg %s — use schema reject or missingArg", path, lit.Value)
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk native", err)
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
						if got := actionenvelope.Names(text, []string{name}); !slices.Equal(got, []string{name}) {
							t.Fatalf("catalog action not recognized exactly once: %s => %v", text, got)
						}
						for _, offered := range [][]string{nil, {}, {"not-offered"}} {
							if got := actionenvelope.Names(text, offered); len(got) != 0 {
								t.Fatalf("unoffered action recognized: %v", got)
							}
						}
						for _, example := range []string{"Example: " + text, "```json\n" + text + "\n```", `{"data":` + text + `}`, `{"tool":"not-offered","args":` + text + `}`} {
							if got := actionenvelope.Names(example, []string{name}); len(got) != 0 {
								t.Fatalf("example or argument data became action: %s => %v", example, got)
							}
						}
					}
				}
			}
		})
	}
}
