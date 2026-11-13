package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/eval/episode"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTrainingContractWritesTheContractOnce(t *testing.T) {
	out := filepath.Join(t.TempDir(), "contract")
	testutil.FailErr(t, "write contract", runEval([]string{"training-contract", "--out", out}))
	raw, err := os.ReadFile(filepath.Join(out, "contract.json"))
	testutil.FailErr(t, "read contract", err)
	var contract episode.TrainingContract
	testutil.FailErr(t, "decode contract", json.Unmarshal(raw, &contract))
	if contract.Schema != episode.ContractSchema || contract.Bundle != episode.BundleSchema {
		t.Fatalf("contract = %+v", contract)
	}
	if err := runEval([]string{"training-contract", "--out", out}); err == nil {
		t.Fatal("an existing contract directory was reused")
	}
	if err := runEval([]string{"training-contract"}); err == nil {
		t.Fatal("a contract without an output was written")
	}
}

func TestTrainingBundleRequiresEveryInput(t *testing.T) {
	for _, args := range [][]string{
		{"training-bundle", "--capture", "c", "--session", "s", "--out", "o"},
		{"training-bundle", "--captures", "c", "--session", "s", "--out", "o"},
		{"training-bundle", "--capture", "c", "--captures", "c", "--out", "o"},
		{"training-bundle", "--capture", "c", "--captures", "c", "--session", "s"},
		{"training-bundle", "--undefined"},
	} {
		if err := runEval(args); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
	missing := t.TempDir()
	if err := runEval([]string{"training-bundle", "--capture", missing, "--captures", missing, "--session", "s", "--out", filepath.Join(missing, "b")}); err == nil {
		t.Fatal("a bundle was written from a capture with no store")
	}
}
