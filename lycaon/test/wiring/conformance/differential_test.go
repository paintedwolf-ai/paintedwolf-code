package conformance_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

// goldenForms are catalog spellings that no reference userland implements, each
// with the golden test of the lane that owns it.
var goldenForms = map[string]string{
	// Only the macOS base64 wraps with -b; its -D decode spelling is tested there too.
	"base64:-b": "internal/tools/native/literal/forms_test.go",
}

func corpus(t *testing.T) []commandconformance.Case {
	t.Helper()
	cases := commandconformance.Cases()
	testutil.FailErr(t, "validate the corpus", commandconformance.Validate(cases))
	return cases
}

func TestCatalogCoverageInConformanceCorpus(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "load command language v1 catalog", err)

	coveredCommands := map[string]bool{}
	coveredForms := map[string]bool{}
	for form := range goldenForms {
		coveredForms[form] = true
	}
	cases := corpus(t)
	for _, c := range contractCases {
		cases = append(cases, c.Case)
	}
	for _, c := range cases {
		for _, cmd := range c.Commands {
			coveredCommands[cmd] = true
		}
		for _, form := range c.Forms {
			coveredForms[form] = true
		}
	}

	var missingCommands, missingForms []string
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
			if key := spec.Name + ":" + form.Spellings[0]; !coveredForms[key] {
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

// TestPortableCommandDifferentialConformance runs every corpus program through the
// command entry and compares it with the stored reference snapshots.
func TestPortableCommandDifferentialConformance(t *testing.T) {
	cases := corpus(t)
	stored, err := commandconformance.LoadSnapshots()
	testutil.FailErr(t, "load reference snapshots", err)
	runner := newConformanceRunner(t, gate.PostureLight, true)
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			refs, err := stored.For(c, runner.seed)
			testutil.FailErr(t, "stored snapshots", err)
			assertMatchesReference(t, c, runner.runCase(t, c), refs)
		})
	}
}

// assertMatchesReference fails unless the case ran to completion natively and
// left what one of its reference userlands left.
func assertMatchesReference(t *testing.T, c commandconformance.Case, obs runtimeObservation, refs map[referenceuserland.ID]commandconformance.Snapshot) {
	t.Helper()
	if obs.TopLevelReject != "" {
		t.Fatalf("case %s top-level reject: %s", c.ID, obs.TopLevelReject)
	}
	if obs.Outcome.Refusal != nil {
		t.Fatalf("case %s refused with code=%s feedback=%q (stdout=%q stderr=%q)",
			c.ID, obs.Outcome.Refusal.Code, obs.Outcome.Refusal.Feedback, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	if obs.Outcome.NativeGaps > 0 {
		t.Fatalf("case %s fell back to an installed program: native_gaps=%d receipts=%v", c.ID, obs.Outcome.NativeGaps, obs.ChildReceipts)
	}
	if obs.Outcome.State != "completed" {
		t.Fatalf("case %s did not complete: state=%s stdout=%q stderr=%q", c.ID, obs.Outcome.State, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	if matched, diffs := commandconformance.Match(obs.streams(), refs); matched == "" {
		t.Fatalf("case %s matches none of %v; receipts=%v%s", c.ID, slices.Sorted(slices.Values(c.References)), obs.ChildReceipts, commandconformance.FormatDiffs(diffs))
	}
}
