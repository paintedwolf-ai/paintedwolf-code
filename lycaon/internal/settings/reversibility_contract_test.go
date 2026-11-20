package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A retired tool keeps its tier only when command still reaches its operation.
func TestRetiredToolsHoldNoToolTier(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "agent-public-retirements.json"))
	testutil.FailErr(t, "read retirements", err)
	var doc struct {
		Vocabularies struct {
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"vocabularies"`
	}
	testutil.FailErr(t, "decode retirements", json.Unmarshal(raw, &doc))
	if len(doc.Vocabularies.Tools) == 0 {
		t.Fatal("no retired tools read")
	}
	tools := tierSet(reversibleTools, recoverableTools, irreversibleTools)
	operations := tierSet(reversibleOperations, recoverableOperations, irreversibleOperations)
	for tool, entry := range doc.Vocabularies.Tools {
		_, operationDeclared := operations[tool]
		if _, declared := tools[tool]; declared {
			t.Errorf("retired tool %s holds a tool tier", tool)
		}
		// A successor list names tools; a content contract replaces a tool with no operation.
		var successors []string
		if json.Unmarshal(entry, &successors) == nil && slices.Equal(successors, []string{"command"}) && !operationDeclared {
			t.Errorf("retired tool %s lost the operation tier command reaches", tool)
		}
	}
}

// TestEveryNativeToolDeclaresATier checks approval classification coverage.
func TestEveryNativeToolDeclaresATier(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)
	if err := cfg.ValidateApprovalReversibility(); err != nil {
		t.Fatalf("approval_reversibility map: %v", err)
	}

	tools := tierSet(reversibleTools, recoverableTools, irreversibleTools)
	operations := tierSet(reversibleOperations, recoverableOperations, irreversibleOperations)
	for _, entry := range []struct {
		names    []string
		declared map[string]struct{}
	}{
		{names: cfg.AllTools(), declared: tools},
		{names: cfg.AllOperations(), declared: operations},
	} {
		for _, name := range entry.names {
			tier, ok := baseToolTier(name)
			want, _ := cfg.Reversibility(name)
			if _, declared := entry.declared[name]; !declared || !ok || TierLabel(tier) != want {
				t.Errorf("approval tier for %s = %q, declared = %v, want %q", name, TierLabel(tier), declared, want)
			}
		}
	}
}
