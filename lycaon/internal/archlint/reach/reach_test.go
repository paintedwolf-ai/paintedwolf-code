package reach_test

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/archlint/reach"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunReportsUnreachableFunctionsKeysAndTables(t *testing.T) {
	dir, err := filepath.Abs("testdata/fixture")
	testutil.FailErr(t, "filepath.Abs failed", err)
	got, err := reach.Run(reach.Config{
		Dir:      dir,
		Patterns: []string{"./cmd/..."},
		Scope:    []string{"./..."},
		GOOS:     []string{"linux", "darwin"},
		Report:   regexp.MustCompile(`^example\.com/fixture/`),
		Skip:     []*regexp.Regexp{regexp.MustCompile(`test$`)},
		Schema:   filepath.Join(dir, "schema.sql"),
	})
	testutil.FailErr(t, "reach.Run failed", err)
	keys := map[string]string{}
	for _, f := range got {
		keys[f.Kind+" "+f.Key] = f.Pos
	}
	want := []string{
		"dead-func example.com/fixture/store.WithTenant",
		"dead-func example.com/fixture/store.memoryRepo.Extra",
		"dead-func example.com/fixture/store.unusedLinux@platform_darwin.go",
		"dead-func example.com/fixture/store.unusedLinux@platform_linux.go",
		"dead-func example.com/fixture/orphan.Unused",
		"unset-context-key example.com/fixture/store.tenantKey",
		"write-only-table audit",
		"write-only-table events",
	}
	for _, k := range want {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing %s in %v", k, keys)
		}
	}
	if len(keys) != len(want) {
		t.Errorf("findings = %v, want exactly %v", keys, want)
	}
	if pos := keys["write-only-table audit"]; pos != "schema.sql:3" {
		t.Errorf("audit position = %q", pos)
	}
}

func TestRunRefusesAProgramWithoutMains(t *testing.T) {
	dir, err := filepath.Abs("testdata/fixture")
	testutil.FailErr(t, "filepath.Abs failed", err)
	if _, err := reach.Run(reach.Config{Dir: dir, Patterns: []string{"./store"}, GOOS: []string{"linux"}}); err == nil {
		t.Fatal("a load with no main package must fail")
	}
	if _, err := reach.Run(reach.Config{Dir: dir, Patterns: []string{"./cmd/..."}}); err == nil {
		t.Fatal("a run with no GOOS must fail")
	}
}
