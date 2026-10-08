package maintainability

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

func TestMaintainabilityPolicyCoversEveryCategory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testutil.CheckoutRoot(t), policyPath))
	testutil.FailErr(t, "read maintainability budgets", err)
	_, err = decodePolicy(raw)
	testutil.FailErr(t, "decode maintainability budgets", err)
}

func TestMaintainabilityPolicyRejectsWhatAReviewerCannotWeigh(t *testing.T) {
	var limits strings.Builder
	for _, name := range suite.CategoryNames() {
		limits.WriteString("    " + name + ": {warn: 400, limit: 600}\n")
	}
	valid := "limits:\n" + limits.String() + "exceptions:\n    source_files:\n        a.go:\n            cap: 700\n            reason: one table per dialect\n"
	if _, err := decodePolicy([]byte(valid)); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	for name, body := range map[string]string{
		"unknown field":      valid + "grandfathered: {}\n",
		"duplicate artifact": strings.Replace(valid, "        a.go:\n", "        a.go:\n            cap: 701\n        a.go:\n", 1),
		"fractional cap":     strings.Replace(valid, "cap: 700", "cap: 700.5", 1),
		"quoted cap":         strings.Replace(valid, "cap: 700", "cap: '700'", 1),
		"cap within limit":   strings.Replace(valid, "cap: 700", "cap: 600", 1),
		"missing reason":     strings.Replace(valid, "reason: one table per dialect", "reason: ''", 1),
		"missing category":   strings.Replace(valid, "    source_files: {warn: 400, limit: 600}\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePolicy([]byte(body)); err == nil {
				t.Fatalf("accepted invalid policy:\n%s", body)
			}
		})
	}
}

func TestMaintainabilityTouchesTypesByTheLinesAChangeEdits(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"Taskfile.yml":            "tasks: {}\n",
		"feature/server.go":       "package feature\n\ntype Server struct{ a, b int }\n\nfunc (s *Server) One() int { return s.a }\n\nfunc helper() int { return 1 }\n",
		"feature/server_extra.go": "package feature\n\nfunc (s *Server) Two() int {\n\treturn s.b\n}\n",
		"web/main.ts":             "import { one } from './one';\nexport const two = one + 1;\n",
		"web/one.ts":              "export const one = 1;\n",
	})
	tree := fixtureTree(t, root)
	sources, err := discoverSources(tree)
	testutil.FailErr(t, "discover fixture sources", err)
	inv, err := measure(t.Context(), tree, sources)
	testutil.FailErr(t, "measure fixture", err)
	const server = "feature/feature.Server"
	for name, tc := range map[string]struct {
		change                   sizebudget.ChangeSet
		fields, methods, entries bool
	}{
		"helper edit":      {change: sizebudget.ChangeSet{Lines: map[string][]int{"feature/server.go": {7}}}},
		"declaration edit": {change: sizebudget.ChangeSet{Lines: map[string][]int{"feature/server.go": {3}}}, fields: true},
		"method edit":      {change: sizebudget.ChangeSet{Lines: map[string][]int{"feature/server_extra.go": {4}}}, methods: true},
		"new file":         {change: sizebudget.ChangeSet{Added: []string{"web/new.ts"}}, entries: true},
		"inspected file":   {change: sizebudget.ChangeSet{Inspect: []string{"feature/server.go"}}, fields: true, methods: true},
	} {
		t.Run(name, func(t *testing.T) {
			isTouched := touched(inv, &tc.change)
			if got := isTouched("go_struct_fields", server); got != tc.fields {
				t.Errorf("fields touched = %v, want %v", got, tc.fields)
			}
			if got := isTouched("go_receiver_lines", server); got != tc.methods {
				t.Errorf("methods touched = %v, want %v", got, tc.methods)
			}
			if got := isTouched("source_directories", "web"); got != tc.entries {
				t.Errorf("directory touched = %v, want %v", got, tc.entries)
			}
		})
	}
	if got := inv.measured["go_receiver_methods"][server]; got != 2 {
		t.Fatalf("receiver methods across files = %d, want 2", got)
	}
}

func TestMaintainabilityArtifactSources(t *testing.T) {
	inv := &inventory{measured: newMeasurements(), sources: map[string][]string{"pkg/p.Server": {"pkg/a.go", "pkg/b.go"}}}
	inv.measured["source_files"]["pkg/a.go"] = 10
	inv.measured["source_directories"]["pkg"] = 2
	inv.measured["go_receiver_lines"]["pkg/p.Server"] = 30
	got := artifactSources(inv)
	if !reflect.DeepEqual(got["source_files"]["pkg/a.go"], []string{"pkg/a.go"}) ||
		!reflect.DeepEqual(got["source_directories"]["pkg"], []string{"pkg"}) ||
		!reflect.DeepEqual(got["go_receiver_lines"]["pkg/p.Server"], []string{"pkg/a.go", "pkg/b.go"}) {
		t.Fatalf("artifact sources = %#v", got)
	}
}
