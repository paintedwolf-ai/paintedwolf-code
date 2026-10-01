package hintregistry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestListBundledRegistry(t *testing.T) {
	entries, err := hintregistry.ListEffective()
	testutil.FailErr(t, "ListStock bundled registry", err)
	if len(entries) < 100 {
		t.Fatalf("expected >=100 codes, got %d", len(entries))
	}
}

func TestListReturnsFlatPolicyBodies(t *testing.T) {
	entries, err := hintregistry.ListEffective()
	testutil.FailErr(t, "list effective", err)
	for _, entry := range entries {
		var body map[string]any
		testutil.FailErr(t, "decode "+entry.Code, yaml.Unmarshal(entry.Body, &body))
		if _, wrapped := body["hint_codes"]; wrapped {
			t.Fatalf("%s retained the registry wrapper", entry.Code)
		}
	}
}

func BenchmarkListEffective(b *testing.B) {
	_, err := hintregistry.ListEffective()
	testutil.FailErr(b, "warm list effective", err)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := hintregistry.ListEffective()
		testutil.FailErr(b, "list effective", err)
	}
}

func TestListRejectsHintCodesFilenameMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WRONG.yaml")
	body := "hint_codes:\n  COMMAND_NOT_ARGV:\n    emit: guard:command_surface\n"
	testutil.FailErr(t, "write mismatched hint file", os.WriteFile(path, []byte(body), 0o600))
	if _, err := hintregistry.List(extpacks.OnDisk(dir)); err == nil {
		t.Fatal("expected filename mismatch error")
	}
}
