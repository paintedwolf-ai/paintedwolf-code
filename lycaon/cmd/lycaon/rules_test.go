package main

// The rules verb: dispatch, flag parsing, and a passing check run.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRulesDispatchFaults(t *testing.T) {
	ctx := context.Background()
	err := runRules(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("runRules(nil) = %v", err)
	}
	err = runRules(ctx, []string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown rules command") {
		t.Fatalf("runRules(bogus) = %v", err)
	}
	err = runRulesTest(ctx, []string{"--bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("runRulesTest(--bogus) = %v", err)
	}
}

func TestRunRulesTestNoRulesFound(t *testing.T) {
	err := runRulesTest(context.Background(), []string{"--project", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no rules found") {
		t.Fatalf("empty project: %v", err)
	}
}

func TestRunRulesTestChecksRule(t *testing.T) {
	dir := t.TempDir()
	rule := filepath.Join(dir, "rule.yaml")
	body := "spec_version: '1.0'\nid: PROBE_RULE\nkind: policy\non: tool.pre_invoke\n" +
		"selector: {}\neffect: warn\nwhen: 'true'\nwhat: test\n"
	if err := os.WriteFile(rule, []byte(body), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}
	// The rules test verb runs the check pipeline over an explicit path.
	if err := runRulesTest(context.Background(), []string{rule, "--json"}); err != nil {
		t.Fatalf("runRulesTest(%q): %v", rule, err)
	}
	// The full verb dispatch reaches runRules through the CLI switch.
	if err := run(context.Background(), []string{"rules", "test", rule}); err != nil {
		t.Fatalf("run(rules test): %v", err)
	}
	if err := run(context.Background(), []string{"rules", "bogus"}); err == nil ||
		!strings.Contains(err.Error(), "unknown rules command") {
		t.Fatalf("run(rules bogus) = %v", err)
	}
}
