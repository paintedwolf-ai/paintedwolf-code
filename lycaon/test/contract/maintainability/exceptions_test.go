package maintainability

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func TestArtifactExceptionsRejectDuplicatesUnknownCategoriesAndMissingReasons(t *testing.T) {
	raw := `{"category":"source_files","artifact":"feature.go","cap":700,"reason":"fixed table"}`
	for _, tc := range []struct {
		name, body       string
		duplicate, valid bool
	}{
		{name: "valid", body: raw, valid: true},
		{name: "unknown category", body: strings.Replace(raw, "source_files", "unknown", 1)},
		{name: "missing reason", body: strings.Replace(raw, "fixed table", "", 1)},
		{name: "duplicate", body: raw, duplicate: true},
		{name: "trailing content", body: raw + " {}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := sizebudget.Policy{Limits: map[string]sizebudget.Limit{}}
			for _, name := range suite.CategoryNames() {
				policy.Limits[name] = sizebudget.Limit{Warn: 400, Limit: 600}
			}
			name := exceptionDirectory + "/one.json"
			tree := &workingTree{paths: []string{name}, bodies: map[string][]byte{name: []byte(tc.body)}}
			if tc.duplicate {
				other := exceptionDirectory + "/two.json"
				tree.paths = append(tree.paths, other)
				tree.bodies[other] = []byte(tc.body)
			}
			err := loadExceptions(tree, &policy)
			if tc.valid {
				testutil.FailErr(t, "load valid exception", err)
			} else if err == nil {
				t.Fatal("invalid exception was accepted")
			}
		})
	}
}
