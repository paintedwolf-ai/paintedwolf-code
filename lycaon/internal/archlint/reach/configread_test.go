package reach_test

import (
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/archlint/reach"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunReportsDecodedKeysNothingReads(t *testing.T) {
	dir, err := filepath.Abs("testdata/configread")
	testutil.FailErr(t, "filepath.Abs failed", err)
	got, err := reach.Run(reach.Config{
		Dir:          dir,
		Patterns:     []string{"./cmd/..."},
		GOOS:         []string{"linux", "darwin"},
		Report:       regexp.MustCompile(`^example\.com/configread/`),
		Product:      []string{"./cmd/app"},
		ConfigFields: true,
	})
	testutil.FailErr(t, "reach.Run failed", err)
	var keys []string
	for _, f := range got {
		if f.Kind == reach.KindUnreadConfigField {
			keys = append(keys, f.Key)
		}
	}
	sort.Strings(keys)
	want := []string{"example.com/configread/conf.Config.Unused", "example.com/configread/conf.Nested.Width"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("unread keys = %q, want %q", keys, want)
	}
}

func TestRunRefusesConfigFieldsWithoutProduct(t *testing.T) {
	dir, err := filepath.Abs("testdata/configread")
	testutil.FailErr(t, "filepath.Abs failed", err)
	if _, err := reach.Run(reach.Config{Dir: dir, Patterns: []string{"./cmd/..."}, GOOS: []string{"linux"}, ConfigFields: true}); err == nil {
		t.Fatal("judged config fields without product roots")
	}
}
