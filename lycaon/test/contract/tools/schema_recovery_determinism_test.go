package contract

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
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
