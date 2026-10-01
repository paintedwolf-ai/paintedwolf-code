package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProjectLicenseIsApache2(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	contractcheck.FailErr(t, "read LICENSE", err)
	text := string(data)
	if !strings.Contains(text, "Apache License") || !strings.Contains(text, "Version 2.0") {
		t.Fatal("root LICENSE must be Apache-2.0")
	}
	for _, want := range []string{
		"Copyright 2026 Painted Wolf LLC",
		"Painted Wolf LLC",
		"Trademarks:",
		"does not grant trademark rights",
		"docs/trademarks.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("root LICENSE missing %q", want)
		}
	}
	for _, bad := range []string{
		"GNU AFFERO GENERAL PUBLIC LICENSE",
		"AGPL-3.0",
		"Chris Beckman",
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("root LICENSE must not contain %q", bad)
		}
	}
}

func TestLycaonRulesLicenseIsCCBY(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "runtime", "scanners", "rules", "lycaon", "LICENSE"))
	contractcheck.FailErr(t, "read lycaon scan rules LICENSE", err)
	text := strings.ToLower(string(data))
	if !strings.Contains(text, "creative commons") || !strings.Contains(text, "attribution 4.0") {
		t.Fatal("lycaon scan rules LICENSE must be CC-BY 4.0")
	}
}

func TestDesignKitLicensesAreCommercialFriendly(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	base := filepath.Join(root, "lycaon", "internal", "browser", "designkit")
	prov, err := os.ReadFile(filepath.Join(base, "provenance.yaml"))
	contractcheck.FailErr(t, "read designkit provenance", err)
	text := string(prov)
	for _, want := range []string{"OFL-1.1", "ISC", "Inter", "Lucide", "commercial"} {
		if !strings.Contains(text, want) && want != "commercial" {
			t.Fatalf("provenance.yaml missing %q", want)
		}
	}
	for _, bad := range []string{"CC-BY-NC", "AGPL", "GPL-3", "non-commercial", "NonCommercial"} {
		if strings.Contains(text, bad) {
			t.Fatalf("design kit provenance must not include %q", bad)
		}
	}
	for _, name := range []string{
		"OFL-inter.txt", "OFL-fraunces.txt", "Lucide-ISC.txt",
	} {
		if _, err := os.Stat(filepath.Join(base, "licenses", name)); err != nil {
			t.Fatalf("missing license file %s: %v", name, err)
		}
	}
	for _, font := range []string{"Inter.woff2", "Fraunces.woff2", "JetBrainsMono.woff2"} {
		if _, err := os.Stat(filepath.Join(base, "fonts", font)); err != nil {
			t.Fatalf("missing font %s: %v", font, err)
		}
	}
}
