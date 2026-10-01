package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Every inventory input is covered by the drift check that gates pull
// requests, so a dependency change cannot land with a stale inventory.
func TestInventoryDriftGatesEveryChange(t *testing.T) {
	t.Parallel()
	repo := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(repo, "scripts/verification-plan.json"))
	testutil.FailErr(t, "read verification plan", err)
	var plan struct {
		Groups map[string][]string `json:"groups"`
	}
	testutil.FailErr(t, "decode verification plan", json.Unmarshal(body, &plan))
	if !slices.Contains(plan.Groups["check:drift"], "codegen:dependency-inventory:check") {
		t.Fatal("check:drift must run codegen:dependency-inventory:check")
	}
}
