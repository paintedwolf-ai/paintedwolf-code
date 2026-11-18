package oardraft_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/oardraft"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A rule as Painted Wolf 1.0.2 shipped it.
const candidate = `oar: '1.0'
id: WEAK_CREDENTIAL_LITERAL
kind: policy
anchor: credential.assignment
requires:
  facts:
  - paintedwolf.credential_fingerprint
  profiles:
  - filesystem
when: paintedwolf.credential_decisions == 0
counter_scope: paintedwolf.credential_fingerprint
on_fire: [increment_counter]
on_error: fail_open
effect: warn
copy:
  what: A literal looks like a weak credential.
  fix: Use a managed reference.
status: stable
x-paintedwolf-category: informational
x-paintedwolf-message: a possible literal credential appears weak
`

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	testutil.FailErr(t, "decode", yaml.Unmarshal(data, &doc))
	return doc
}

func TestUpgradeRewritesACandidateRuleInTheOAR10Shape(t *testing.T) {
	out, upgraded, err := oardraft.Upgrade([]byte(candidate))
	testutil.FailErr(t, "upgrade", err)
	if !upgraded {
		t.Fatal("a candidate rule was not upgraded")
	}
	want := map[string]any{
		"spec_version": "1.0", "id": "WEAK_CREDENTIAL_LITERAL", "kind": "policy", "on": "credential.assignment",
		"selector": map[string]any{}, "requires_facts": []any{"paintedwolf.credential_fingerprint"},
		"when": "paintedwolf.credential_decisions == 0", "x-paintedwolf-counter-scope": "paintedwolf.credential_fingerprint",
		"on_fire": []any{"increment_counter"}, "on_error": "fail_open", "effect": "warn",
		"what": "A literal looks like a weak credential.", "fix": "Use a managed reference.",
		"x-paintedwolf-category": "informational", "message": "a possible literal credential appears weak",
	}
	if got := decode(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("upgraded rule:\n%v\nwant\n%v", got, want)
	}
	again, upgradedAgain, err := oardraft.Upgrade(out)
	testutil.FailErr(t, "upgrade again", err)
	if upgradedAgain || string(again) != string(out) {
		t.Fatal("an OAR 1.0 rule was rewritten")
	}
}

func TestUpgradeRenamesTheToolSelector(t *testing.T) {
	out, _, err := oardraft.Upgrade([]byte("oar: '1.0'\nid: X\nkind: schema\nanchor: tool.rejected\nselector:\n  tool: [ask_user]\neffect: block\n"))
	testutil.FailErr(t, "upgrade", err)
	if got := decode(t, out)["selector"]; !reflect.DeepEqual(got, map[string]any{"tools": []any{"ask_user"}}) {
		t.Fatalf("selector = %v", got)
	}
}

func TestUpgradeKeepsJSONDocumentsJSON(t *testing.T) {
	out, upgraded, err := oardraft.Upgrade([]byte(`{"oar":"1.0","id":"X","kind":"policy","anchor":"tool.rejected","effect":"warn"}`))
	testutil.FailErr(t, "upgrade", err)
	var doc map[string]any
	if !upgraded || json.Unmarshal(out, &doc) != nil || doc["spec_version"] != "1.0" || doc["on"] != "tool.rejected" {
		t.Fatalf("JSON rule = %s", out)
	}
}

func TestUpgradeRefusesWhatOAR10Removed(t *testing.T) {
	base := "oar: '1.0'\nid: X\nkind: policy\nanchor: tool.rejected\n"
	for name, doc := range map[string]string{
		"transform effect":   base + "effect: transform\n",
		"transform block":    base + "effect: block\ntransform: {redact: []}\n",
		"enforcement off":    base + "effect: block\nenforcement: 'off'\n",
		"overrides":          base + "effect: block\noverrides: [OTHER]\n",
		"fact selector":      base + "effect: block\nselector:\n  session_role: [worker]\n",
		"unknown requires":   base + "effect: block\nrequires:\n  detectors: [x]\n",
		"unknown copy field": base + "effect: block\ncopy:\n  footer: x\n",
		"other version":      "oar: '0.9'\nid: X\nkind: policy\nanchor: tool.rejected\neffect: block\n",
	} {
		_, _, err := oardraft.Upgrade([]byte(doc))
		var refusal *oardraft.Unconvertible
		if !errors.As(err, &refusal) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

func TestUpgradeLeavesOtherDocumentsAlone(t *testing.T) {
	for _, doc := range []string{"spec_version: '1.0'\noar: x\n", "hint_codes: {}\n", "- a\n", "not: [yaml\n"} {
		out, upgraded, err := oardraft.Upgrade([]byte(doc))
		if err != nil || upgraded || string(out) != doc {
			t.Errorf("%q: upgraded=%v err=%v", doc, upgraded, err)
		}
	}
}
