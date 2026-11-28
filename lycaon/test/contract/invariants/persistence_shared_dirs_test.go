package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
)

var persistenceSharedDirsTree = map[string]string{
	"lycaon/internal/blobstore/blobstore.go": "package blobstore\n\nimport \"os\"\n\ntype Store struct{}\n\n" +
		"func (Store) Put() {}\nfunc (Store) Maintain() {}\n" +
		"func (Store) removeBlob(dir string) { _ = os.RemoveAll(dir) }\n" +
		"func (Store) releaseStage(path string) { _ = os.Remove(path) }\n",
	"lycaon/internal/contentblob/contentblob.go": "package contentblob\n",
	"lycaon/internal/blobcache/cas.go":           "package blobcache\n",
	"lycaon/internal/bloblifecycle/remove.go":    "package bloblifecycle\n\nimport \"os\"\n\nfunc RemoveTree(p string) error { return os.RemoveAll(p) }\n",
}

// TestPersistenceSharedDirsInvariantFailClosed is the standard fixture
// self-test for GB10 I7 static row: clean, seeded violation, renamed anchor,
// stale exemption, empty selection, and broken source.
func TestPersistenceSharedDirsInvariantFailClosed(t *testing.T) {
	t.Parallel()
	rule := invariantstest.Rule(t, forbiddencalls.Family, "persistence-no-shared-dir-removal-outside-pruners")

	withStaleExemption := rule
	withStaleExemption.Exempt = []invariants.Exemption{{
		Key:    "internal/blobstore/stale.go:unused",
		Reason: "fixture stale exemption check",
	}}

	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  invariantstest.Outcome
	}{
		"clean": {
			rule:  rule,
			files: persistenceSharedDirsTree,
			want:  invariantstest.Passes,
		},
		"seeded violation releaseStage removes parent dir": {
			rule: rule,
			files: invariantstest.With(persistenceSharedDirsTree, map[string]string{
				"lycaon/internal/blobstore/blobstore.go": "package blobstore\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n)\n\ntype Store struct{}\n\n" +
					"func (Store) Put() {}\nfunc (Store) Maintain() {}\n" +
					"func (Store) releaseStage(path string) { _ = os.Remove(path); _ = os.Remove(filepath.Dir(path)) }\n",
			}),
			want: invariantstest.Violates,
		},
		"seeded violation non-pruner RemoveAll": {
			rule: rule,
			files: invariantstest.With(persistenceSharedDirsTree, map[string]string{
				"lycaon/internal/contentblob/wipe.go": "package contentblob\n\nimport \"os\"\n\nfunc wipe(d string) { _ = os.RemoveAll(d) }\n",
			}),
			want: invariantstest.Violates,
		},
		"renamed anchor": {
			rule: rule,
			files: invariantstest.With(persistenceSharedDirsTree, map[string]string{
				"lycaon/internal/blobstore/blobstore.go": "package blobstore\n\ntype Store struct{}\n\nfunc (Store) PutRenamed() {}\nfunc (Store) Maintain() {}\n",
			}),
			want: invariantstest.MissesAnchor,
		},
		"stale exemption": {
			rule:  withStaleExemption,
			files: persistenceSharedDirsTree,
			want:  invariantstest.StaleExemption,
		},
		"empty selection": {
			rule:  rule,
			files: nil,
			want:  invariantstest.Fails,
		},
		"broken source": {
			rule: rule,
			files: invariantstest.With(persistenceSharedDirsTree, map[string]string{
				"lycaon/internal/blobstore/broken.go": "package blobstore\n\nfunc (\n",
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
