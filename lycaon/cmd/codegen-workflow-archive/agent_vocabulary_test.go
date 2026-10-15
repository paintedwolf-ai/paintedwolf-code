package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentVocabularyRequiresStableSignaturesAndRetirementSuccessors(t *testing.T) {
	for _, tc := range []struct {
		name, kind, oldType, newType, want string
		removed, retired, reused, missing  bool
	}{
		{name: "unchanged", oldType: "bool", newType: "bool"},
		{name: "type change", oldType: "bool", newType: "string", want: "signature"},
		{name: "undeclared removal", oldType: "bool", removed: true, want: "without a named successor"},
		{name: "named retirement", oldType: "bool", removed: true, retired: true},
		{name: "missing successor", oldType: "bool", removed: true, retired: true, missing: true, want: "unavailable successor"},
		{name: "reused name", oldType: "bool", newType: "bool", retired: true, reused: true, want: "was reused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := map[string]factSignature{"old": {Name: "old", Type: tc.oldType}}
			after := map[string]factSignature{"new": {Name: "new", Type: "bool"}}
			if !tc.removed {
				after["old"] = factSignature{Name: "old", Type: tc.newType}
			}
			retired := map[string][]string{}
			if tc.retired {
				retired["old"] = []string{"new"}
			}
			if tc.missing {
				delete(after, "new")
			}
			err := checkVocabulary("host fact", before, after, retired, true)
			if tc.want == "" {
				testutil.FailErr(t, "check fact lineage", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("lineage=%v, want %s", err, tc.want)
			}
		})
	}
	if err := checkVocabulary("rejection code", map[string]struct{}{}, map[string]struct{}{}, map[string][]string{"OLD": {}}, false); err == nil {
		t.Fatal("retirement without successor passed")
	}
}

func TestAgentVocabularyReadsStructuredDeclarations(t *testing.T) {
	tree := treeRelease{capabilityPath: []byte("host_facts:\n - name: observed\n   type: bool\n   observation: host\n"), "schemas/guidance_registry.json": []byte(`{"hint_codes":{"EXAMPLE":{"summary":"display copy"}}}`)}
	facts, err := factVocabulary(tree)
	testutil.FailErr(t, "read facts", err)
	if facts["observed"].Observation != "host" || facts["observed"].Type != "bool" {
		t.Fatalf("signature=%+v", facts)
	}
	codes, err := codeVocabulary(tree)
	testutil.FailErr(t, "read codes", err)
	if _, ok := codes["EXAMPLE"]; !ok {
		t.Fatal("missing structured rejection code")
	}
	tree[capabilityPath] = append(tree[capabilityPath], []byte(" - name: observed\n   type: string\n")...)
	if _, err := factVocabulary(tree); err == nil {
		t.Fatal("duplicate fact passed")
	}
	tree["schemas/guidance_registry.json"] = []byte("invalid")
	if _, err := codeVocabulary(tree); err == nil {
		t.Fatal("malformed registry passed")
	}
	delete(tree, capabilityPath)
	if _, err := factVocabulary(tree); err == nil {
		t.Fatal("missing capability document passed")
	}
}

func TestRetirementCatalogIsClosedAndVersioned(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"format_version":2,"facts":{},"rejection_codes":{}}`,
		`{"format_version":1,"facts":{},"rejection_codes":{},"extra":true}`,
		`{"format_version":1,"facts":{},"rejection_codes":{}} {}`,
	} {
		if _, err := parseRetirements([]byte(raw)); err == nil {
			t.Fatalf("invalid retirement catalog passed: %s", raw)
		}
	}
	_, err := parseRetirements([]byte(`{"format_version":1,"facts":{},"rejection_codes":{}}`))
	testutil.FailErr(t, "parse closed retirement catalog", err)
	source := treeRelease{"schemas/agent-public-retirements.json": []byte(`{"format_version":1,"facts":{"paintedwolf.forgotten":["paintedwolf.successor"]},"rejection_codes":{}}`)}
	if err := checkAgentVocabulary(source, source); err == nil || !strings.Contains(err.Error(), "released retirement") {
		t.Fatalf("retirement removal: %v", err)
	}
}
