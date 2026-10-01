package rules

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestCompileGateRulesRejectsInvalidSelection(t *testing.T) {
	for _, cfg := range []*OpengrepGatesConfig{nil, {}, {Vendor: []string{"config/runtime/scanners/rules/vendor/"}}, {Vendor: []string{"../outside.yaml"}}, {Vendor: []string{"config/missing.yaml"}}} {
		if _, err := CompileGateRules(cfg, ""); err == nil {
			t.Fatalf("invalid selection accepted: %+v", cfg)
		}
	}
	cfg, err := LoadOpengrepGates()
	testutil.FailErr(t, "load", err)
	cfg.Lycaon = append(cfg.Lycaon, cfg.Lycaon[0])
	if _, err := CompileGateRules(cfg, ""); err == nil {
		t.Fatal("duplicate selection accepted")
	}
}

func TestBundleAnchorScopes(t *testing.T) {
	var combined struct {
		Rules []yaml.Node `yaml:"rules"`
	}
	for _, tc := range []struct{ yaml, prefix string }{
		{"id: first\npaths: &paths {include: ['*.a']}\nagain: *paths\n", "first_"},
		{"id: second\npaths: &paths {include: ['*.b']}\nagain: *paths\n", "second_"},
	} {
		var node yaml.Node
		testutil.FailErr(t, "decode", yaml.Unmarshal([]byte(tc.yaml), &node))
		namespaceAnchors(&node, tc.prefix)
		combined.Rules = append(combined.Rules, *node.Content[0])
	}
	raw, err := yaml.Marshal(combined)
	testutil.FailErr(t, "encode bundle", err)
	var decoded struct {
		Rules []struct {
			Again struct {
				Include []string `yaml:"include"`
			} `yaml:"again"`
		} `yaml:"rules"`
	}
	testutil.FailErr(t, "decode bundle", yaml.Unmarshal(raw, &decoded))
	if decoded.Rules[0].Again.Include[0] != "*.a" || decoded.Rules[1].Again.Include[0] != "*.b" {
		t.Fatalf("alias scope changed: %s", raw)
	}
}

func TestContentAddressedBundleIsExact(t *testing.T) {
	cfg, err := LoadOpengrepGates()
	testutil.FailErr(t, "load", err)
	want, err := CompileGateRules(cfg, "")
	testutil.FailErr(t, "compile", err)
	paths, err := MaterializeGateRules(cfg, "", t.TempDir())
	testutil.FailErr(t, "materialize", err)
	if len(paths) != 1 || !filepath.IsAbs(paths[0]) {
		t.Fatalf("bundle paths = %v", paths)
	}
	got, err := os.ReadFile(paths[0])
	testutil.FailErr(t, "read", err)
	if !bytes.Equal(want, got) {
		t.Fatal("runtime bundle differs from conformance selection")
	}
}

func TestGateRuleFilesRejectAmbiguousOrNoisyCandidates(t *testing.T) {
	valid := "rules:\n- id: candidate\n  languages: [javascript]\n  severity: ERROR\n  pattern: sink(...)\n"
	for name, files := range map[string][]GateRuleFile{
		"empty":                  nil,
		"second document":        {{Name: "candidate", Data: []byte(valid + "---\nrules: []\n")}},
		"unknown envelope":       {{Name: "candidate", Data: []byte(valid + "overrides: true\n")}},
		"duplicate rule":         {{Name: "first", Data: []byte(valid)}, {Name: "second", Data: []byte(valid)}},
		"informational security": {{Name: "candidate", Data: []byte(strings.Replace(valid, "ERROR", "INFO", 1))}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CompileGateRuleFiles(files); err == nil {
				t.Fatal("invalid candidate selection accepted")
			}
		})
	}
}

func TestGateRuleFilesPreserveModelQualifiersAndAliases(t *testing.T) {
	source := `rules:
- id: candidate
  languages: [javascript]
  severity: ERROR
  mode: taint
  pattern-sources:
  - &model
    pattern: source()
    exact: true
    model-type: reviewed.module
    model-singleton: true
  - *model
  pattern-sinks:
  - pattern: sink(...)
`
	bundle, err := CompileGateRuleFiles([]GateRuleFile{{Name: "candidate", Data: []byte(source)}})
	testutil.FailErr(t, "compile model candidate", err)
	var decoded struct {
		Rules []struct {
			Sources []struct {
				Model  string `yaml:"model-type"`
				Shared bool   `yaml:"model-singleton"`
			} `yaml:"pattern-sources"`
		} `yaml:"rules"`
	}
	testutil.FailErr(t, "decode model candidate", yaml.Unmarshal(bundle, &decoded))
	if len(decoded.Rules) != 1 || len(decoded.Rules[0].Sources) != 2 {
		t.Fatalf("candidate structure changed: %s", bundle)
	}
	for _, model := range decoded.Rules[0].Sources {
		if model.Model != "reviewed.module" || !model.Shared {
			t.Fatalf("model qualifier or alias lost: %s", bundle)
		}
	}
}
