package secretmint

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialContributionValidation(t *testing.T) {
	for _, body := range []string{
		"version: 2\nenv_keys: [ACCESS_VALUE]",
		"version: 1",
		"version: 1\nexcluded_keys: [password]",
		"version: 1\nsql_mint: [{pattern: SELECT}]",
		"version: 1\nthreshold: 4",
		"version: 1\nenv_keys: [x]\n---\nenv_keys: [y]",
		"version: 1\nenv_keys: [x]\nenv_keys: [y]",
		"version: 1\nsurfaces: {external: {kind: script}}",
		"version: 1\nsurfaces: {external: {kind: content}}",
		"version: 1\nsurfaces: {external: {kind: command, content_arg: payload}}",
		"version: 1\nkey_terms: [api-key]",
		"version: 1\nkey_term_pairs: [[api]]",
		"version: 1\nkey_term_pairs: [[api, key, extra]]",
		"version: 1\nvalue_flags: [-p]",
		"version: 1\nvalue_flags: [--]",
		"version: 1\nenv_keys: ['*']",
		"version: 1\ncommand_images: [{image: tool}]",
		"version: 1\ncommand_images: [{image: /bin/tool, value: last_positional}]",
		"version: 1\ncommand_images: [{image: tool, value: first_positional}]",
		"version: 1\ncommand_images: [{image: tool, value_flags: [--x], require_flags: [enable]}]",
		"version: 1\nenv_keys: [" + strings.Repeat("x", 129) + "]",
		"version: 1\nenv_keys: [" + strings.Repeat("x,", 257) + "]",
		"#" + strings.Repeat("x", maxContributionBytes),
	} {
		if _, err := ParseContribution([]byte(body)); err == nil {
			t.Errorf("accepted invalid contribution: %.160s", body)
		}
	}
}

func TestCredentialContributionsComposeWithoutReplacingBaseline(t *testing.T) {
	first, err := ParseContribution([]byte(`version: 1
surfaces:
  write: {kind: content, content_arg: extra}
  vendor: {kind: content, content_arg: body}
  vendor_run: {kind: command}
  vendor_input: {kind: terminal, content_arg: text}
env_keys: [ACCESS_VALUE]
file_keys: [ACCESS_VALUE, CONFIG_VALUE]
value_flags: [--access-value]
key_terms: [passcode]
key_term_pairs: [[login, material]]
command_images:
  - image: vendorcli
    require_flags: [--create]
    value_flags: [-x]
  - image: casecli
    require_flags: [--Create]
    value_flags: [-P]
`))
	testutil.FailErr(t, "parse contribution", err)
	second, err := ParseContribution([]byte(`version: 1
surfaces:
  vendor: {kind: content, content_arg: other}
env_keys: [SECOND_VALUE, next_token]
`))
	testutil.FailErr(t, "parse second contribution", err)
	ins, err := Compile([]Contribution{first, second})
	testutil.FailErr(t, "compile contributions", err)
	cases := []struct {
		tool string
		args map[string]any
		want int
	}{
		{"write", map[string]any{"content": "password=one", "extra": "ACCESS_VALUE=two"}, 2},
		{"vendor", map[string]any{"body": "ACCESS_VALUE=one", "other": "password=two"}, 2},
		{"vendor", map[string]any{"body": `{"nested": {"CONFIG_VALUE": "one"}, "ACCESS_VALUE": "two"}`}, 2},
		{"unknown", map[string]any{"nested": map[string]any{"ACCESS_VALUE": "one", "SECOND_VALUE": "two", "loginMaterial": "three", "passcode": "four"}}, 4},
		{"command", map[string]any{"command": "vendorcli --create -x one --access-value two"}, 2},
		{"command", map[string]any{"command": "vendorcli -x one"}, 0},
		{"vendor_run", map[string]any{"command": "vendorcli --create -x one"}, 1},
		{"vendor_input", map[string]any{"text": "vendorcli --create -x one\n"}, 1},
		{"command", map[string]any{"command": "casecli --Create -P one"}, 1},
		{"command", map[string]any{"command": "casecli --Create -p one"}, 0},
		{"command", map[string]any{"command": "casecli --create -P one"}, 0},
		{"command", map[string]any{"command": "vendorcli -x one -- --create"}, 0},
		{"unknown", map[string]any{"next_token": "one", "password": "${VALUE}", "ACCESS_VALUE": "{{paintedwolf-secret:123e4567-e89b-42d3-a456-426614174000}}"}, 0},
	}
	for _, tc := range cases {
		if hits := ins.Inspect(tc.tool, tc.args); len(hits) != tc.want {
			t.Errorf("%s: got assignments %v, want %d", tc.tool, assignments(hits), tc.want)
		}
	}
	// Compiled views own their inputs and remain usable after source mutation.
	first.CommandImages[0].ValueFlags[0] = "-z"
	first.Surfaces["vendor"] = Surface{Kind: "content", ContentArg: "absent"}
	if hits := ins.Inspect("command", map[string]any{"command": "vendorcli --create -x one"}); len(hits) != 1 {
		t.Fatal("source mutation changed compiled command recognition")
	}
	if hits := ins.Inspect("vendor", map[string]any{"body": "password=one"}); len(hits) != 1 {
		t.Fatal("source mutation changed compiled surface")
	}
}

func TestCredentialContributionOverlapProducesOneCandidate(t *testing.T) {
	c, err := ParseContribution([]byte("version: 1\nenv_keys: [password]\nsurfaces: {write: {kind: content, content_arg: content}}"))
	testutil.FailErr(t, "parse overlap", err)
	ins, err := Compile([]Contribution{c, c})
	testutil.FailErr(t, "compile overlap", err)
	if hits := ins.Inspect("write", map[string]any{"content": "password=one"}); len(hits) != 1 {
		t.Fatalf("overlapping declarations duplicated observations: %v", assignments(hits))
	}
}

func TestCredentialJSONAliasesProduceOneObservationPerAssignment(t *testing.T) {
	first, err := ParseContribution([]byte("version: 1\nenv_keys: [ACCESS_VALUE]"))
	testutil.FailErr(t, "parse structured alias", err)
	second, err := ParseContribution([]byte("version: 1\nfile_keys: [access_value]"))
	testutil.FailErr(t, "parse authored alias", err)
	ins, err := Compile([]Contribution{first, second})
	testutil.FailErr(t, "compile overlapping aliases", err)
	if hits := ins.Inspect("write", map[string]any{"content": `{"Access_Value": "one"}`}); len(hits) != 1 {
		t.Fatalf("case-insensitive aliases duplicated one JSON assignment: %v", assignments(hits))
	}
}
