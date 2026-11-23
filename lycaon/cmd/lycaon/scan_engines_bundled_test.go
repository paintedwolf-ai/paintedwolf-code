package main

import (
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
)

func TestVerifyBundledRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--root"}, {"--root", "/tmp", "extra"}, {"--unknown"}} {
		if err := runVerifyBundledEngine(args); err == nil {
			t.Fatalf("accepted invalid verification arguments: %v", args)
		}
	}
}

func TestRunScanEnginesListCheckAndValidate(t *testing.T) {
	moduleRoot := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	if err := runScanEnginesList(moduleRoot, projectDir); err != nil {
		t.Fatalf("runScanEnginesList failed: %v", err)
	}
	if err := runScanEnginesValidate(moduleRoot, projectDir, nil); err != nil {
		t.Fatalf("runScanEnginesValidate failed: %v", err)
	}
	_ = runScanEnginesCheck(moduleRoot, projectDir, []string{"--dry-run"})
}
