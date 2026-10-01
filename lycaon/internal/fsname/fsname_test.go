package fsname

import "testing"

func TestCanonicalBasename(t *testing.T) {
	cases := map[string]string{
		"approvals.yaml":        "approvals.yaml",
		"approvals.yaml::$DATA": "approvals.yaml",
		"approvals.yaml:stream": "approvals.yaml",
		"AGENTS.md.":            "AGENTS.md",
		"AGENTS.md ":            "AGENTS.md",
		"AGENTS.md. .":          "AGENTS.md",
		"mcp.yaml.::$DATA":      "mcp.yaml",
		"plain":                 "plain",
		"":                      "",
	}
	for in, want := range cases {
		if got := CanonicalBasename(in); got != want {
			t.Errorf("CanonicalBasename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEqualBasename(t *testing.T) {
	// Every respelling folds to the target; an unrelated name does not.
	match := []string{
		"approvals.yaml",
		"Approvals.YAML",
		"approvals.yaml::$DATA",
		"approvals.yaml.",
		"approvals.yaml ",
		"APPROVALS.yaml:s",
	}
	for _, c := range match {
		if !EqualBasename(c, "approvals.yaml") {
			t.Errorf("EqualBasename(%q, approvals.yaml) = false, want true", c)
		}
	}
	for _, c := range []string{"approvals.yml", "limits.yaml", "approvals.yamlx"} {
		if EqualBasename(c, "approvals.yaml") {
			t.Errorf("EqualBasename(%q, approvals.yaml) = true, want false", c)
		}
	}
}
