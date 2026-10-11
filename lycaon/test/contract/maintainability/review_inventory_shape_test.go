package maintainability

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func measuredDependencyInventory(t *testing.T, body string) *inventory {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"feature/one.ts": "", "feature/two.ts": "", "feature/types.ts": "",
	})
	inv, err := measure(t.Context(), fixtureTree(t, root), []source{{
		Path: "feature/main.ts", Language: "typescript", Body: []byte(body),
	}})
	testutil.FailErr(t, "measure dependency identities", err)
	return inv
}

func requiredInventoryShape(t *testing.T, inv *inventory, category, id string) string {
	t.Helper()
	shape, err := inventoryShape(inv, category, id)
	testutil.FailErr(t, "fingerprint measured identities", err)
	return shape
}

func TestDependencyShapeUsesResolvedIdentities(t *testing.T) {
	base := measuredDependencyInventory(t, `import {one} from './one'; import type {Type} from './types';`)
	shape := requiredInventoryShape(t, base, "ts_local_dependencies", "feature/main.ts")
	equivalent := measuredDependencyInventory(t, `
export type {Type} from './types.ts';
export {one as renamed} from './one.ts';
const deferred = import('./one');
`)
	if requiredInventoryShape(t, equivalent, "ts_local_dependencies", "feature/main.ts") != shape {
		t.Fatal("import ordering, aliases, or duplicate resolved paths renewed the review")
	}
	replaced := measuredDependencyInventory(t, `import {two} from './two'; import type {Type} from './types';`)
	if replaced.measured["ts_local_dependencies"]["feature/main.ts"] != base.measured["ts_local_dependencies"]["feature/main.ts"] {
		t.Fatal("replacement fixture changed dependency count")
	}
	if requiredInventoryShape(t, replaced, "ts_local_dependencies", "feature/main.ts") == shape {
		t.Fatal("same-count dependency replacement preserved the review shape")
	}
	empty := measuredDependencyInventory(t, `export const answer = 42;`)
	if requiredInventoryShape(t, empty, "ts_local_dependencies", "feature/main.ts") == "" {
		t.Fatal("measured empty dependency set has no shape")
	}
}

func TestDirectoryShapeUsesMeasuredImmediateMembers(t *testing.T) {
	measureFiles := func(sources []source) *inventory {
		t.Helper()
		inv, err := measure(t.Context(), fixtureTree(t, t.TempDir()), sources)
		testutil.FailErr(t, "measure directory members", err)
		return inv
	}
	base := []source{
		{Path: "feature/one.ts", Language: "typescript", Body: []byte("export const one = 1;")},
		{Path: "feature/one.test.ts", Language: "typescript", Test: true, Body: []byte("const test = 1;")},
		{Path: "feature/nested/two.ts", Language: "typescript", Body: []byte("export const two = 2;")},
	}
	original := measureFiles(base)
	sourceShape := requiredInventoryShape(t, original, "source_directories", "feature")
	testShape := requiredInventoryShape(t, original, "test_directories", "feature")
	changed := append([]source{}, base...)
	changed[0].Body = []byte("// Changed implementation.\nexport const one = 3;")
	changed[2].Path = "feature/nested/renamed.ts"
	stable := measureFiles([]source{changed[2], changed[1], changed[0]})
	if requiredInventoryShape(t, stable, "source_directories", "feature") != sourceShape {
		t.Fatal("body, order, or nested membership changes renewed the immediate directory")
	}
	changed[0].Path = "feature/replacement.ts"
	replaced := measureFiles(changed)
	if replaced.measured["source_directories"]["feature"] != original.measured["source_directories"]["feature"] {
		t.Fatal("replacement fixture changed directory count")
	}
	if requiredInventoryShape(t, replaced, "source_directories", "feature") == sourceShape {
		t.Fatal("same-count source replacement preserved the directory shape")
	}
	if requiredInventoryShape(t, replaced, "test_directories", "feature") != testShape {
		t.Fatal("source replacement changed the test directory shape")
	}
	changed[1].Path = "feature/replacement.test.ts"
	if requiredInventoryShape(t, measureFiles(changed), "test_directories", "feature") == testShape {
		t.Fatal("same-count test replacement preserved the directory shape")
	}
}

func TestInventoryShapeRefusesMissingIdentityEvidence(t *testing.T) {
	for _, inv := range []*inventory{nil, {}, {dependencies: map[string][]string{"feature/main.ts": nil}}} {
		if _, err := inventoryShape(inv, "ts_local_dependencies", "feature/main.ts"); err == nil {
			t.Fatal("missing dependency identities produced a shape")
		}
		if _, err := inventoryShape(inv, "source_directories", "feature"); err == nil {
			t.Fatal("missing directory identities produced a shape")
		}
	}
	if _, err := inventoryShape(&inventory{}, "source_files", "feature/main.ts"); err == nil {
		t.Fatal("unsupported category produced a shape")
	}
}

func TestInventoryReviewsRenewOnSameCountIdentityReplacement(t *testing.T) {
	for _, category := range []string{"ts_local_dependencies", "source_directories", "test_directories"} {
		t.Run(category, func(t *testing.T) {
			tree, policy, _, review := reviewFixture(t)
			var base, changed *inventory
			id := "feature/main.ts"
			if category == "ts_local_dependencies" {
				base = measuredDependencyInventory(t, `import './one'; import './types';`)
				changed = measuredDependencyInventory(t, `import './two'; import './types';`)
			} else {
				id = "feature"
				files := []source{
					{Path: "feature/one.ts", Language: "typescript", Test: category == "test_directories", Body: []byte("const one = 1;")},
					{Path: "feature/two.ts", Language: "typescript", Test: category == "test_directories", Body: []byte("const two = 2;")},
				}
				var err error
				base, err = measure(t.Context(), tree, files)
				testutil.FailErr(t, "measure initial members", err)
				files[0].Path = "feature/replacement.ts"
				changed, err = measure(t.Context(), tree, files)
				testutil.FailErr(t, "measure replaced members", err)
			}
			policy.Limits = map[string]sizebudget.Limit{category: {Warn: 1, Limit: 4}}
			review.Category, review.Artifact, review.Ceiling = category, id, 2
			review.ReviewedShape = requiredInventoryShape(t, base, category, id)
			putReview(t, tree, reviewDirectory+"/inventory.json", review)
			for _, tc := range []struct {
				inv   *inventory
				state string
			}{{base, "retained"}, {changed, "shape_changed"}} {
				report := trackingReport(policy, tc.inv, func(string, string) bool { return false })
				testutil.FailErr(t, "attach measured inventory review", attachReviews(tree, policy, tc.inv, report))
				var actual string
				for _, artifact := range report.Artifacts {
					if artifact.Category == category && artifact.ID == id && artifact.Review != nil {
						actual = artifact.Review.State
					}
				}
				if actual != tc.state {
					t.Fatalf("review for %s = %q, want %s", category, actual, tc.state)
				}
			}
		})
	}
}
