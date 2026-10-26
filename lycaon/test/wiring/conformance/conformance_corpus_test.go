package conformance_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type v1SpecificExpectation struct {
	Validate       func(*testing.T, runtimeObservation)
	ExpectedStderr string
	ExpectedStatus int
	ExpectedStdout string
	AllowExternal  bool
	ExpectedFiles  map[string]*string
}

func strPtr(s string) *string { return &s }

// v1SpecificCases are corpus entries that intentionally exercise Painted Wolf v1
// language or built-in contracts that do not exist in POSIX/GNU/BSD bash.
var v1SpecificCases = map[string]v1SpecificExpectation{
	"inspect-domain-builtins": {ExpectedStatus: 0, Validate: verifyInspectDomainOutputs},
	"mutation-domain-builtins": {
		ExpectedStatus: 0,
		ExpectedStdout: "",
		ExpectedFiles: map[string]*string{
			"sub/a.txt":   strPtr("EDITED\nsecond\n"),
			"sub/b.txt":   strPtr("REPLACED\nthird\n"),
			"config.json": strPtr("{\"enabled\":true,\"version\":1,\"items\":[\"a\",\"b\"]}\n"),
		},
	},
	"query-jq-options-and-transform": {
		ExpectedStatus: 0,
		ExpectedStdout: "a\nb\n{\"greeting\":7}\n5\njq_e=1\n{\"enabled\":true,\"version\":1,\"items\":[\"a\",\"b\"]}\n",
		ExpectedFiles:  map[string]*string{"config.json": strPtr("{\"enabled\":true,\"version\":1,\"items\":[\"a\",\"b\"]}\n")},
	},
	"git-diff-separated-u-flag": {ExpectedStatus: 0, ExpectedStdout: "diff --git a/sub/a.txt b/sub/a.txt\nindex 66a52ee..48f6562 100644\n--- a/sub/a.txt\n+++ b/sub/a.txt\n@@ -2 +2,2 @@ first\n second\n+modified\n", ExpectedFiles: map[string]*string{"sub/a.txt": strPtr("first\nsecond\nmodified\n")}},
	"run3-paths-and-scratch":    {ExpectedStatus: 0, ExpectedStdout: "paths-ok\n"},
	"run3-native-discovery":     {ExpectedStatus: 0, ExpectedStdout: "jq is a native built-in\njq\n"},
	"run3-command-not-found":    {ExpectedStatus: 0, ExpectedStdout: "missing:127\n", ExpectedStderr: "bash: pw-missing-program-039: command not found\n"},

	"lang-same-file-redirect-publish": {
		ExpectedStatus: 0,
		ExpectedStdout: "FIRST\nSECOND\n",
		ExpectedFiles: map[string]*string{
			"sub/a.txt": strPtr("FIRST\nSECOND\n"),
		},
	},
	"paths-native-path-conversion": {
		ExpectedStatus: 0,
		ExpectedStdout: "native-path-ok\n",
	},
	"process-external-escape-hatch": {
		ExpectedStatus: 0,
		ExpectedStdout: "ext-ok\n",
		AllowExternal:  true,
	},
}

func TestCatalogCoverageInConformanceCorpus(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "load command language v1 catalog", err)

	coveredCommands := map[string]bool{}
	coveredForms := map[string]bool{}
	for _, c := range corpus.Cases {
		for _, cmd := range c.Commands {
			coveredCommands[cmd] = true
		}
		for _, form := range c.Forms {
			coveredForms[form] = true
		}
	}

	var missingCommands []string
	var missingForms []string
	for _, spec := range catalog.Commands {
		if !coveredCommands[spec.Name] {
			missingCommands = append(missingCommands, spec.Name)
		}
		for _, alias := range spec.Aliases {
			if !coveredCommands[alias] {
				missingCommands = append(missingCommands, fmt.Sprintf("%s (alias of %s)", alias, spec.Name))
			}
		}
		for _, form := range spec.Forms {
			if len(form.Spellings) == 0 {
				continue
			}
			key := spec.Name + ":" + form.Spellings[0]
			if !coveredForms[key] {
				missingForms = append(missingForms, key)
			}
		}
	}
	if len(missingCommands) > 0 {
		t.Fatalf("conformance corpus is missing %d catalog commands/aliases: %v", len(missingCommands), missingCommands)
	}
	if len(missingForms) > 0 {
		t.Fatalf("conformance corpus is missing %d catalog option forms: %v", len(missingForms), missingForms)
	}
}

func TestPortableCommandDifferentialConformance(t *testing.T) {
	runner := newConformanceRunner(t)
	for _, c := range runner.corpus.Cases {
		t.Run(c.ID, func(t *testing.T) {
			obs := runner.runCase(t, c)
			if v1Exp, ok := v1SpecificCases[c.ID]; ok {
				verifyV1SpecificExpectation(t, c, obs, v1Exp)
				return
			}
			assertMatchesReference(t, c, obs)
		})
	}
}

func verifyV1SpecificExpectation(t *testing.T, c fixtureCase, obs runtimeObservation, exp v1SpecificExpectation) {
	t.Helper()
	if obs.TopLevelReject != "" {
		t.Fatalf("case %s top-level reject: %s", c.ID, obs.TopLevelReject)
	}
	if obs.Outcome.Refusal != nil {
		t.Fatalf("case %s refused: %+v", c.ID, obs.Outcome.Refusal)
	}
	if !exp.AllowExternal && obs.Outcome.NativeGaps > 0 {
		t.Fatalf("case %s unexpected native_gaps=%d", c.ID, obs.Outcome.NativeGaps)
	}
	if obs.Outcome.State != "completed" || obs.Outcome.Status != exp.ExpectedStatus {
		t.Fatalf("case %s status = %d, want %d (stdout=%q stderr=%q)", c.ID, obs.Outcome.Status, exp.ExpectedStatus, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	if obs.Outcome.Stderr != exp.ExpectedStderr {
		t.Fatalf("case %s stderr = %q, want %q", c.ID, obs.Outcome.Stderr, exp.ExpectedStderr)
	}
	if exp.Validate != nil {
		exp.Validate(t, obs)
	} else if obs.Outcome.Stdout != exp.ExpectedStdout {
		t.Fatalf("case %s stdout = %q, want %q", c.ID, obs.Outcome.Stdout, exp.ExpectedStdout)
	}
	if exp.ExpectedFiles != nil && !filesEqual(obs.Files, exp.ExpectedFiles) {
		t.Fatalf("case %s files = %s, want %s", c.ID, formatFiles(obs.Files), formatFiles(exp.ExpectedFiles))
	}
}

func assertMatchesReference(t *testing.T, c fixtureCase, obs runtimeObservation) {
	t.Helper()
	if obs.TopLevelReject != "" {
		t.Fatalf("case %s top-level reject: %s", c.ID, obs.TopLevelReject)
	}
	if obs.Outcome.Refusal != nil {
		t.Fatalf("case %s refused with code=%s feedback=%q (stdout=%q stderr=%q)",
			c.ID, obs.Outcome.Refusal.Code, obs.Outcome.Refusal.Feedback, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	if obs.Outcome.NativeGaps > 0 {
		t.Fatalf("case %s fell back to external execution with native_gaps=%d receipts=%v (stdout=%q stderr=%q)",
			c.ID, obs.Outcome.NativeGaps, obs.ChildReceipts, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	gnuMatch := matchesSnapshot(obs, c.GNU)
	bsdMatch := matchesSnapshot(obs, c.BSD)
	if !gnuMatch && !bsdMatch {
		t.Fatalf("case %s diverged from both GNU and BSD fixtures:\n  got: status=%d state=%s stdout=%q stderr=%q files=%v receipts=%v\n  gnu: status=%d stdout=%q stderr=%q files=%v\n  bsd: status=%d stdout=%q stderr=%q files=%v",
			c.ID,
			obs.Outcome.Status, obs.Outcome.State, obs.Outcome.Stdout, obs.Outcome.Stderr, formatFiles(obs.Files), obs.ChildReceipts,
			c.GNU.Status, c.GNU.Stdout, c.GNU.Stderr, formatFiles(c.GNU.Files),
			c.BSD.Status, c.BSD.Stdout, c.BSD.Stderr, formatFiles(c.BSD.Files),
		)
	}
}

func matchesSnapshot(obs runtimeObservation, ref fixtureSnapshot) bool {
	if obs.Outcome.State != "completed" || obs.Outcome.Status != ref.Status {
		return false
	}
	if obs.Outcome.Stdout != ref.Stdout || obs.Outcome.Stderr != ref.Stderr {
		return false
	}
	if !filesEqual(obs.Files, ref.Files) {
		return false
	}
	return true
}

func filesEqual(a, b map[string]*string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if va == nil || vb == nil {
			if va != vb {
				return false
			}
			continue
		}
		if normalizeUnifiedDiffHeaders(*va) != normalizeUnifiedDiffHeaders(*vb) {
			return false
		}
	}
	return true
}

func normalizeUnifiedDiffHeaders(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			if tab := strings.IndexByte(line, '\t'); tab >= 0 {
				lines[i] = line[:tab]
			}
		}
	}
	return strings.Join(lines, "\n")
}

func formatFiles(files map[string]*string) string {
	if len(files) == 0 {
		return "{}"
	}
	var parts []string
	for k, v := range files {
		if v == nil {
			parts = append(parts, k+":<nil>")
		} else {
			parts = append(parts, fmt.Sprintf("%s:%q", k, *v))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
