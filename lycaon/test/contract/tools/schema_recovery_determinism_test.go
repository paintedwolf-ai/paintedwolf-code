package contract

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestSchemaRecoveryDeterminism tests that validating bad arguments across multiple
// iterations consistently yields the exact same ToolReject data, ensuring map order
// randomness doesn't leak into OAR facts.
func TestSchemaRecoveryDeterminism(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)

	cmdSchema := cfg.Tools["command"].Schema

	// Setup a misnested argument
	args := map[string]any{
		"command": "echo 1",
		// Put connection directly at root instead of under capability_request
		"connection": map[string]any{
			"url": "http://localhost",
		},
	}

	var firstJSON []byte
	tc := tools.ToolContext{}

	// Test 100 iterations to catch map randomness
	for i := 0; i < 100; i++ {
		// Clone args to randomize map layout in memory
		clonedArgs := make(map[string]any)
		for k, v := range args {
			clonedArgs[k] = v
		}

		reject := tools.ValidateCallArguments("command", clonedArgs, cmdSchema, tc)
		if reject == nil {
			t.Fatal("expected rejection for misnested args")
		}

		bytes, err := json.Marshal(reject.Data)
		if err != nil {
			t.Fatalf("failed to marshal reject data: %v", err)
		}

		if i == 0 {
			firstJSON = bytes
		} else if string(bytes) != string(firstJSON) {
			t.Fatalf("determinism failed on iteration %d.\nFirst: %s\nDiff:  %s", i, firstJSON, bytes)
		}
	}
}

// Recovery alternatives must be callable on the rejected request, even when the
// compiled plan permits additional schemas to be loaded on a later request.
func TestSchemaRecoverySuggestionUsesOfferedTools(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(contractcheck.RepoRoot(t), "lycaon/config/packs/painted-wolf/platform/tools/schemas"))
	contractcheck.FailErr(t, "load tool schemas", err)
	for _, tool := range []string{"edit", "replace_lines", "code_rewrite", "read"} {
		for _, surface := range []struct {
			name      string
			plan      toolsurface.Plan
			offered   []string
			wantWrite bool
		}{
			{"offered", toolsurface.Compile([]string{tool, "write"}, nil), []string{tool, "write"}, true},
			{"deferred", toolsurface.Compile([]string{tool, "request_tools"}, []string{"write"}), []string{tool, "request_tools"}, false},
			{"plan_only", toolsurface.Compile([]string{tool, "write"}, nil), []string{tool}, false},
			{"unavailable", toolsurface.Compile([]string{tool}, nil), []string{tool}, false},
			{"empty_offer", toolsurface.Compile([]string{tool, "write"}, nil), []string{}, false},
			{"unknown_offer", toolsurface.Compile([]string{tool, "write"}, nil), nil, false},
		} {
			t.Run(tool+"/"+surface.name, func(t *testing.T) {
				meta, ok := cfg.ToolMeta(tool)
				if !ok {
					t.Fatalf("missing schema for %s", tool)
				}
				tc := tools.ToolContext{Turn: tools.InvocationTurn{TurnToolPlan: surface.plan, TurnOfferedToolNames: surface.offered}}
				reject := tools.ValidateCallArguments(tool, map[string]any{}, meta.ArgsSchema, tc)
				if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
					t.Fatalf("expected argument rejection for %s: %#v", tool, reject)
				}
				want := ""
				if surface.wantWrite && tool != "read" {
					want = "write"
				}
				if got, _ := reject.Data["suggested_tool"].(string); got != want {
					t.Fatalf("suggested_tool = %q, want %q", got, want)
				}
			})
		}
	}
}
