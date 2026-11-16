package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncReaderFactsGeneratesAndChecksStockCatalog(t *testing.T) {
	t.Parallel()
	moduleRoot, _, err := resolvePaths(filepath.Clean(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("resolvePaths: %v", err)
	}
	committedPath := filepath.Join(moduleRoot, "internal", "prompts", "reader_facts.generated.go")
	if err := syncReaderFacts(moduleRoot, committedPath, true); err != nil {
		t.Fatalf("syncReaderFacts check committed: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "reader_facts.generated.go")
	if err := syncReaderFacts(moduleRoot, outPath, false); err != nil {
		t.Fatalf("syncReaderFacts write: %v", err)
	}
	generated, err := os.ReadFile(outPath) // #nosec G304 -- temp test file
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	committed, err := os.ReadFile(committedPath) // #nosec G304 -- committed repo file
	if err != nil {
		t.Fatalf("read committed file: %v", err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatal("generated reader_facts.generated.go differs from committed file")
	}

	if err := os.WriteFile(outPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	if err := syncReaderFacts(moduleRoot, outPath, true); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale error, got %v", err)
	}
}

func TestParseFamilyPatternAndFieldNames(t *testing.T) {
	t.Parallel()
	prefix, nested, err := parseFamilyPattern("profile_has_{tool}")
	if err != nil || prefix != "profile_has_" || nested {
		t.Fatalf("parseFamilyPattern flat = (%q, %v, %v)", prefix, nested, err)
	}
	prefix, nested, err = parseFamilyPattern("mints.{kind}")
	if err != nil || prefix != "mints" || !nested {
		t.Fatalf("parseFamilyPattern nested = (%q, %v, %v)", prefix, nested, err)
	}
	if _, _, err := parseFamilyPattern("invalid"); err == nil {
		t.Fatal("expected error for invalid family pattern")
	}

	cases := map[string]string{
		"surface_id":             "SurfaceID",
		"runner_direct_ip_tools": "RunnerDirectIPTools",
		"native_edit_limit_mib":  "NativeEditLimitMiB",
		"http_proxy_url":         "HTTPProxyURL",
		"caps.vision":            "CapsVision",
	}
	for in, want := range cases {
		if got := goFieldNameForFact(in); got != want {
			t.Errorf("goFieldNameForFact(%q) = %q, want %q", in, got, want)
		}
	}
}
