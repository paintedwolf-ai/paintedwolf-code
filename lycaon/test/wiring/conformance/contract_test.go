package conformance_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/test/commandconformance"
)

// contractCase is a program whose result the command language defines itself, so
// it has an expected result rather than a reference userland.
type contractCase struct {
	commandconformance.Case
	Status int
	Stdout string
	Stderr string
	// Files are the expected contents of the tracked paths.
	Files map[string]string
	// Validate replaces the stdout comparison for typed native results.
	Validate func(*testing.T, runtimeObservation)
	// External allows the program to launch an installed binary.
	External bool
}

var contractCases = []contractCase{
	{
		Case: commandconformance.Case{
			ID:          "run3-native-discovery",
			Description: "Discovery describes a native command as native.",
			Commands:    []string{"type", "command"},
			Program:     `type jq; command -v jq`,
		},
		Stdout: "jq is a native built-in\njq\n",
	},
	{
		Case: commandconformance.Case{
			ID:          "run3-paths-and-scratch",
			Description: "PWD and the scratch variables are usable absolute paths.",
			Commands:    []string{"test", "printf", "pwd"},
			Program:     `test "$PWD" = "$(pwd)" && test -d "$TMPDIR" && test -d "$SCRATCH_DIR" && printf 'paths-ok\n'`,
		},
		Stdout: "paths-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID:          "lang-same-file-redirect-publish",
			Description: "A redirect to a file the pipeline reads publishes the complete output on success, where a shell truncates first.",
			Commands:    []string{"cat", "tr"},
			Program:     `cat sub/a.txt | tr 'a-z' 'A-Z' > sub/a.txt && cat sub/a.txt`,
			Tracked:     []string{"sub/a.txt"},
		},
		Stdout: "FIRST\nSECOND\n",
		Files:  map[string]string{"sub/a.txt": "FIRST\nSECOND\n"},
	},
	{
		Case: commandconformance.Case{
			ID:          "query-jq-same-file-transform",
			Description: "A jq transform redirected to its own input publishes the transformed document.",
			Commands:    []string{"jq", "cat"},
			Program:     `cat config.json | jq -c '.enabled = true' > config.json && cat config.json`,
			Tracked:     []string{"config.json"},
		},
		Stdout: "{\"enabled\":true,\"version\":1,\"items\":[\"a\",\"b\"]}\n",
		Files:  map[string]string{"config.json": "{\"enabled\":true,\"version\":1,\"items\":[\"a\",\"b\"]}\n"},
	},
	{
		// Under the language's pipefail, a producer ended by SIGPIPE because its
		// reader finished does not fail the pipeline; PIPESTATUS keeps its 141.
		// bash with pipefail exits 141 here.
		Case: commandconformance.Case{
			ID:          "pipefail-reader-ends-producer",
			Description: "A reader that stops early ends a long producer without failing the pipeline.",
			Commands:    []string{"seq", "head", "grep", "printf"},
			Forms:       []string{"head:-n", "grep:-m"},
			Program:     `seq 1 100000000 | head -3; seq 1 100000 | grep -m1 5; printf 'status:%s pipe:%s\n' "$?" "${PIPESTATUS[*]}"`,
		},
		Stdout: "1\n2\n3\n5\nstatus:0 pipe:141 0\n",
	},
	{
		Case: commandconformance.Case{
			ID:          "inspect-domain-builtins",
			Description: "The outline, repo-map, survey-repo and summarize built-ins return typed results.",
			Commands:    []string{"outline", "repo-map", "survey-repo", "summarize"},
			Forms:       []string{"outline:--path", "repo-map:--path", "repo-map:--depth", "summarize:--path", "summarize:--focus"},
			Program:     `outline --path code.go; repo-map --path . --depth 2; survey-repo --path . --depth 1; summarize --path code.go --focus Alpha`,
		},
		Validate: verifyInspectDomainOutputs,
	},
	{
		Case: commandconformance.Case{
			ID:          "paths-native-path-conversion",
			Description: "native_path converts a path with and without -0.",
			Commands:    []string{"native_path"},
			Forms:       []string{"native_path:-0"},
			Program:     `p=$(native_path -- sub/a.txt) && test -f "$p" && native_path -0 -- sub/a.txt >/dev/null && printf 'native-path-ok\n'`,
		},
		Stdout: "native-path-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID:          "mutation-domain-builtins",
			Description: "The edit, replace-lines, code-rewrite and jq-edit built-ins change files.",
			Commands:    []string{"edit", "replace-lines", "code-rewrite", "jq-edit"},
			Forms: []string{
				"edit:--path", "edit:--old-string", "edit:--new-string", "edit:--replace-all",
				"replace-lines:--path", "replace-lines:--start", "replace-lines:--end", "replace-lines:--content",
				"code-rewrite:--pattern", "code-rewrite:--rewrite", "code-rewrite:--path", "code-rewrite:--recursive", "code-rewrite:--dry-run",
				"jq-edit:--path", "jq-edit:--query", "jq-edit:--format",
			},
			Program: `edit --path sub/a.txt --old-string first --new-string EDITED --replace-all && replace-lines --path sub/b.txt --start 1 --end 1 --content REPLACED && code-rewrite --pattern 'OldName' --rewrite 'NewName' --path sub/nested/deep.go --dry-run && jq-edit --path config.json --query '.enabled = true' --format json`,
			Tracked: []string{"sub/a.txt", "sub/b.txt", "config.json"},
		},
		Files: map[string]string{
			"sub/a.txt":   "EDITED\nsecond\n",
			"sub/b.txt":   "REPLACED\nthird\n",
			"config.json": "{\"enabled\":true,\"version\":1,\"items\":[\"a\",\"b\"]}\n",
		},
	},
	{
		Case: commandconformance.Case{
			ID:          "process-external-escape-hatch",
			Description: "external -- runs the installed binary with a native built-in's name.",
			Commands:    []string{"external"},
			Program:     `external -- printf 'ext-ok\n'`,
		},
		Stdout:   "ext-ok\n",
		External: true,
	},
	{
		Case: commandconformance.Case{
			ID:          "git-diff-separated-u-flag",
			Description: "git diff accepts the context length as a separate argument.",
			Commands:    []string{"git diff"},
			Forms:       []string{"git diff:-U"},
			Program:     `printf 'modified\n' >> sub/a.txt && git diff -U 1 sub/a.txt`,
			Tracked:     []string{"sub/a.txt"},
			Git:         true,
		},
		Stdout: "diff --git a/sub/a.txt b/sub/a.txt\nindex 66a52ee..48f6562 100644\n--- a/sub/a.txt\n+++ b/sub/a.txt\n@@ -2 +2,2 @@ first\n second\n+modified\n",
		Files:  map[string]string{"sub/a.txt": "first\nsecond\nmodified\n"},
	},
}

func TestCommandLanguageContracts(t *testing.T) {
	runner := newConformanceRunner(t, gate.PostureLight, true)
	for _, c := range contractCases {
		t.Run(c.ID, func(t *testing.T) {
			verifyContract(t, c, runner.runCase(t, c.Case))
		})
	}
}

func verifyContract(t *testing.T, c contractCase, obs runtimeObservation) {
	t.Helper()
	if obs.TopLevelReject != "" {
		t.Fatalf("top-level reject: %s", obs.TopLevelReject)
	}
	if obs.Outcome.Refusal != nil {
		t.Fatalf("refused: %+v", obs.Outcome.Refusal)
	}
	if !c.External && obs.Outcome.NativeGaps > 0 {
		t.Fatalf("unexpected native_gaps=%d", obs.Outcome.NativeGaps)
	}
	if obs.Outcome.State != "completed" || obs.Outcome.Status != c.Status {
		t.Fatalf("state %s status %d, want completed %d (stdout=%q stderr=%q)", obs.Outcome.State, obs.Outcome.Status, c.Status, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	if obs.Outcome.Stderr != c.Stderr {
		t.Fatalf("stderr = %q, want %q", obs.Outcome.Stderr, c.Stderr)
	}
	if c.Validate != nil {
		c.Validate(t, obs)
	} else if obs.Outcome.Stdout != c.Stdout {
		t.Fatalf("stdout = %q, want %q", obs.Outcome.Stdout, c.Stdout)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Files)) {
		if f := obs.Files[name]; f == nil || string(f.Data) != c.Files[name] {
			t.Fatalf("file %s = %+v, want %q", name, f, c.Files[name])
		}
	}
}
