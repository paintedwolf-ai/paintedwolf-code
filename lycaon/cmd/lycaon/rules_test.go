package main

// The rules verb: dispatch, flag parsing, and a passing check run.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
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

func TestRulesUpgradeRewritesCandidateRulesAndNamesTheRest(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "KEEP.yaml")
	redact := filepath.Join(dir, "REDACT.yaml")
	final := filepath.Join(dir, "FINAL.yaml")
	files := map[string]string{
		keep:   "oar: '1.0'\nid: KEEP\nkind: policy\nanchor: tool.rejected\neffect: warn\n",
		redact: "oar: '1.0'\nid: REDACT\nkind: policy\nanchor: tool.post_invoke\neffect: transform\n",
		final:  "spec_version: \"1.0\"\nid: FINAL\nkind: policy\non: tool.rejected\nselector: {}\neffect: warn\n",
	}
	for path, body := range files {
		testutil.FailErr(t, "write "+path, os.WriteFile(path, []byte(body), 0o600))
	}
	var out bytes.Buffer
	err := runRulesUpgrade([]string{keep, redact, final}, &out)
	if err == nil || !strings.Contains(err.Error(), "REDACT.yaml") || !strings.Contains(out.String(), "upgraded "+keep) {
		t.Fatalf("err=%v out=%s", err, &out)
	}
	if got, _ := os.ReadFile(keep); !strings.Contains(string(got), `spec_version: "1.0"`) {
		t.Fatalf("candidate rule not rewritten:\n%s", got)
	}
	for _, path := range []string{redact, final} {
		if got, _ := os.ReadFile(path); string(got) != files[path] {
			t.Fatalf("%s changed:\n%s", path, got)
		}
	}
	if err := runRulesUpgrade(nil, &out); err == nil {
		t.Fatal("upgrade without paths succeeded")
	}
	if err := runRulesUpgrade([]string{filepath.Join(dir, "absent.yaml")}, &out); err == nil {
		t.Fatal("upgrade of a missing file succeeded")
	}
}
