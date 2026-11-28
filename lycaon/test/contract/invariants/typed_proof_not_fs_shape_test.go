package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
)

var typedProofTree = map[string]string{
	"lycaon/internal/session/workercompletion/worker_artifact.go": "package workercompletion\n\nfunc Prove() bool { return true }\n",
	"lycaon/internal/session/closeoutassembly/closeout.go":        "package closeoutassembly\n\nfunc Assemble() {}\n",
	"lycaon/internal/workflow/gates/gates.go":                     "package gates\n\nfunc Evaluate() bool { return true }\n",
}

// TestTypedProofNotFSShapeInvariantFailClosed is the standard fixture self-test
// for GB10 I8 static row: clean, seeded violation, renamed anchor, stale
// exemption, empty selection, and broken source.
func TestTypedProofNotFSShapeInvariantFailClosed(t *testing.T) {
	t.Parallel()
	rule := invariantstest.Rule(t, forbiddencalls.Family, "completion-and-gates-never-read-dir-as-proof")

	withStaleExemption := rule
	withStaleExemption.Exempt = []invariants.Exemption{{
		Key:    "internal/session/workercompletion/stale.go",
		Reason: "fixture stale exemption check",
	}}

	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  invariantstest.Outcome
	}{
		"clean": {
			rule:  rule,
			files: typedProofTree,
			want:  invariantstest.Passes,
		},
		"seeded violation os.ReadDir": {
			rule: rule,
			files: invariantstest.With(typedProofTree, map[string]string{
				"lycaon/internal/session/workercompletion/workspace_files.go": "package workercompletion\n\nimport \"os\"\n\nfunc hasFiles(dir string) bool { entries, _ := os.ReadDir(dir); return len(entries) > 0 }\n",
			}),
			want: invariantstest.Violates,
		},
		"seeded violation filepath.WalkDir": {
			rule: rule,
			files: invariantstest.With(typedProofTree, map[string]string{
				"lycaon/internal/workflow/gates/scan.go": "package gates\n\nimport \"path/filepath\"\n\nfunc scan(dir string) { _ = filepath.WalkDir(dir, nil) }\n",
			}),
			want: invariantstest.Violates,
		},
		"renamed anchor": {
			rule: rule,
			files: map[string]string{
				"lycaon/internal/session/workercompletion/worker_artifact.go": "package workercompletion\n",
				"lycaon/internal/session/closeoutassembly/closeout.go":        "package closeoutassembly\n",
			},
			want: invariantstest.MissesAnchor,
		},
		"stale exemption": {
			rule:  withStaleExemption,
			files: typedProofTree,
			want:  invariantstest.StaleExemption,
		},
		"empty selection": {
			rule:  rule,
			files: nil,
			want:  invariantstest.Fails,
		},
		"broken source": {
			rule: rule,
			files: invariantstest.With(typedProofTree, map[string]string{
				"lycaon/internal/workflow/gates/broken.go": "package gates\n\nfunc (\n",
			}),
			want: invariantstest.Fails,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, tc.files), tc.rule), tc.want, 1)
		})
	}
}
