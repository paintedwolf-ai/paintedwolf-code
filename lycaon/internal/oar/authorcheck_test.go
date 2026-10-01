package oar

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// authorCheckPaths writes a rule (and optionally fixtures) into a temp pack and
// returns the policy dir, so each case exercises the same discovery an author's
// pack layout gets.
func authorCheckPaths(t *testing.T, rule string, fixtures map[string]string) string {
	t.Helper()
	root := t.TempDir()
	policy := filepath.Join(root, "policy")
	if err := os.MkdirAll(policy, 0o750); err != nil {
		t.Fatalf("mkdir policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(policy, "AUTHOR_CHECK_RULE.yaml"), []byte(rule), 0o600); err != nil {
		t.Fatalf("write rule: %v", err)
	}
	if len(fixtures) > 0 {
		dir := filepath.Join(root, "conformance")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir conformance: %v", err)
		}
		for name, body := range fixtures {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatalf("write fixture %s: %v", name, err)
			}
		}
	}
	return policy
}

const authorCheckRule = `oar: "1.0"
id: AUTHOR_CHECK_RULE
kind: policy
anchor: tool.pre_invoke
selector:
  tool: [write]
requires:
  profiles: [session, tool]
when: session_posture == "spec"
effect: block
copy:
  title: Author check rule
  what: A write tool was called.
  cause: The session is in the spec posture.
  why: Spec posture does not change files.
  fix: Leave spec before writing.
  instead: Branch on Code AUTHOR_CHECK_RULE
x-paintedwolf-emit: rule:author_check
x-paintedwolf-message: Writes are blocked in spec.
`

const authorCheckFiringFixture = `{
  "name": "fires",
  "rule": {
    "oar": "1.0",
    "id": "AUTHOR_CHECK_RULE",
    "kind": "policy",
    "anchor": "tool.pre_invoke",
    "selector": { "tool": ["write"] },
    "requires": { "profiles": ["session", "tool"] },
    "when": "session_posture == \"spec\"",
    "effect": "block",
    "x-paintedwolf-emit": "rule:author_check"
  },
  "input": { "anchor": "tool.pre_invoke", "facts": { "tool": "write", "session_posture": "spec" } },
  "expected": { "decision": "block", "code": "AUTHOR_CHECK_RULE" }
}`

func authorCheckOpts(t *testing.T) RuleCheckOptions {
	t.Helper()
	return RuleCheckOptions{
		SchemaDir:         filepath.Join("..", "..", "..", "schemas"),
		AnchorCatalogPath: filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml"),
	}
}

func TestCheckRuleFiles_PassingRuleWithFixtures(t *testing.T) {
	policy := authorCheckPaths(t, authorCheckRule, map[string]string{"fires.json": authorCheckFiringFixture})
	got, err := CheckRuleFiles([]string{policy}, authorCheckOpts(t))
	if err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results = %d want 1: %+v", len(got), got)
	}
	if !got[0].OK() {
		t.Fatalf("expected pass, got %+v", got[0])
	}
	if got[0].Scenarios != 1 {
		t.Fatalf("scenarios = %d want 1", got[0].Scenarios)
	}
}

func TestCheckRuleFiles_ScenarioMismatch(t *testing.T) {
	wrong := strings.Replace(authorCheckFiringFixture, `"decision": "block"`, `"decision": "allow"`, 1)
	policy := authorCheckPaths(t, authorCheckRule, map[string]string{"fires.json": wrong})
	got, err := CheckRuleFiles([]string{policy}, authorCheckOpts(t))
	if err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	if len(got) != 1 || got[0].Code != RuleCheckScenarioMismatch {
		t.Fatalf("expected scenario_mismatch, got %+v", got)
	}
}

func TestCheckRuleFiles_UnknownFactIsLoadError(t *testing.T) {
	bad := strings.Replace(authorCheckRule, `when: session_posture == "spec"`, "when: no_such_fact", 1)
	policy := authorCheckPaths(t, bad, nil)
	got, err := CheckRuleFiles([]string{policy}, authorCheckOpts(t))
	if err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	if len(got) != 1 || got[0].Code != RuleCheckLoadError {
		t.Fatalf("expected one load_error, got %+v", got)
	}
	if !strings.Contains(got[0].Detail, "no_such_fact") {
		t.Fatalf("detail does not name the bad fact: %q", got[0].Detail)
	}
}

func TestCheckRuleFiles_LoadError(t *testing.T) {
	bad := strings.Replace(authorCheckRule, "kind: policy", "kind: nonsense", 1)
	policy := authorCheckPaths(t, bad, nil)
	got, err := CheckRuleFiles([]string{policy}, authorCheckOpts(t))
	if err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	if len(got) != 1 || got[0].Code != RuleCheckLoadError {
		t.Fatalf("expected load_error, got %+v", got)
	}
}

func TestCheckRuleFiles_SingleFileIgnoresSiblings(t *testing.T) {
	policy := authorCheckPaths(t, authorCheckRule, nil)
	broken := strings.Replace(authorCheckRule, "id: AUTHOR_CHECK_RULE", "id: SIBLING_RULE", 1)
	broken = strings.Replace(broken, `when: session_posture == "spec"`, "when: no_such_fact", 1)
	if err := os.WriteFile(filepath.Join(policy, "SIBLING_RULE.yaml"), []byte(broken), 0o600); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	got, err := CheckRuleFiles([]string{filepath.Join(policy, "AUTHOR_CHECK_RULE.yaml")}, authorCheckOpts(t))
	if err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	for _, r := range got {
		if r.RuleID == "SIBLING_RULE" {
			t.Fatalf("naming one file reported a sibling's problem: %+v", r)
		}
	}
}

// TestCheckRuleFiles_ReadOnly proves the verb writes nothing: an author runs it
// against a pack they are still editing.
func TestCheckRuleFiles_ReadOnly(t *testing.T) {
	policy := authorCheckPaths(t, authorCheckRule, map[string]string{"fires.json": authorCheckFiringFixture})
	root := filepath.Dir(policy)
	before := snapshotTree(t, root)
	if _, err := CheckRuleFiles([]string{policy}, authorCheckOpts(t)); err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	after := snapshotTree(t, root)
	if len(before) != len(after) {
		t.Fatalf("tree changed: %v -> %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("tree changed at %d: %q -> %q", i, before[i], after[i])
		}
	}
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

// Inline scenarios are part of the rule's copy contract.
const authorCheckRuleWithScenarios = authorCheckRule + `x-paintedwolf-scenarios:
  - id: default
    vars:
      tool: write
      profile: implementer
    expect_contains:
      - "Leave spec before writing."
`

func TestCheckRuleFiles_RunsScenariosDeclaredOnTheRule(t *testing.T) {
	setupAuthorCheckRenderer(t)
	policy := authorCheckPaths(t, authorCheckRuleWithScenarios, nil)
	got, err := CheckRuleFiles([]string{policy}, authorCheckOpts(t))
	if err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results = %d want 1: %+v", len(got), got)
	}
	if got[0].Scenarios != 1 {
		t.Fatalf("scenarios = %d, want the one the rule declares: %+v", got[0].Scenarios, got[0])
	}
	if !got[0].OK() {
		t.Fatalf("expected pass, got %+v", got[0])
	}
}

func TestCheckRuleFiles_DeclaredScenarioMismatchFails(t *testing.T) {
	setupAuthorCheckRenderer(t)
	rule := strings.Replace(authorCheckRuleWithScenarios,
		`      - "Leave spec before writing."`, `      - "words the copy never says"`, 1)
	policy := authorCheckPaths(t, rule, nil)
	got, err := CheckRuleFiles([]string{policy}, authorCheckOpts(t))
	if err != nil {
		t.Fatalf("CheckRuleFiles: %v", err)
	}
	if len(got) != 1 || got[0].Code != RuleCheckScenarioMismatch {
		t.Fatalf("want a scenario mismatch, got %+v", got)
	}
	if !strings.Contains(got[0].Detail, "words the copy never says") {
		t.Fatalf("detail should name what was missing: %+v", got[0])
	}
}

// setupAuthorCheckRenderer installs the guidance renderer the host and the CLI
// verb both wire before rendering a rule's copy.
func setupAuthorCheckRenderer(t *testing.T) {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(
		prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	t.Cleanup(func() { guidance.SetGuidanceRenderer(nil) })
}
