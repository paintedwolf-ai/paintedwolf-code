package catalog

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledScannerCatalogLoads(t *testing.T) {
	cat, err := LoadScannerCatalog()
	if err != nil {
		testutil.FailErr(t, "load bundled scanner catalog", err)
	}
	entries := cat.Entries()
	if len(entries) == 0 {
		t.Fatal("bundled scanner catalog is empty")
	}
	for _, entry := range entries {
		if _, ok := cat.Entry(entry.ID); !ok {
			t.Fatalf("entry %q missing from byID index", entry.ID)
		}
		// Every row includes an installation reference.
		if strings.TrimSpace(entry.DocsURL) == "" {
			t.Fatalf("entry %q has no docs_url — with no install channel a row would leave the user nowhere to go", entry.ID)
		}
		if len(entry.Probe) == 0 {
			t.Fatalf("entry %q has no probe command for check", entry.ID)
		}
	}
}

func TestScannerDefinitionConvertsToScannerEntry(t *testing.T) {
	cat, err := LoadScannerCatalog()
	if err != nil {
		testutil.FailErr(t, "load bundled scanner catalog", err)
	}
	for _, definition := range cat.Entries() {
		id := definition.ID
		entry, ok := cat.ScannerEntryFor(id)
		if !ok {
			t.Fatalf("ScannerEntryFor(%q) not found", id)
		}
		if entry.Driver != DriverExternal {
			t.Fatalf("%q: driver = %q, want %q", id, entry.Driver, DriverExternal)
		}
		if entry.EnabledOrDefault() {
			t.Fatalf("%q: catalog adds must start disabled", id)
		}
		if err := validateScannerEntry(entry); err != nil {
			testutil.FailErr(t, "validate catalog-derived entry "+id, err)
		}
	}
}

// Exit contracts survive catalog conversion.
func TestScannerCatalogCarriesExitContractIntoScannerEntry(t *testing.T) {
	cat, err := LoadScannerCatalog()
	if err != nil {
		testutil.FailErr(t, "load bundled scanner catalog", err)
	}
	var declared int
	for _, definition := range cat.Entries() {
		if len(definition.OkExitCodes) == 0 {
			continue
		}
		declared++
		entry, ok := cat.ScannerEntryFor(definition.ID)
		if !ok {
			t.Fatalf("ScannerEntryFor(%q) not found", definition.ID)
		}
		if len(entry.OkExitCodes) != len(definition.OkExitCodes) {
			t.Fatalf("%q: ok_exit_codes lost in conversion: catalog %v, entry %v",
				definition.ID, definition.OkExitCodes, entry.OkExitCodes)
		}
		for i, code := range definition.OkExitCodes {
			if entry.OkExitCodes[i] != code {
				t.Fatalf("%q: ok_exit_codes[%d] = %d, want %d",
					definition.ID, i, entry.OkExitCodes[i], code)
			}
		}
	}
	if declared == 0 {
		t.Fatal("no catalog row declares ok_exit_codes; the exit contract would be untested")
	}
}

func TestScannerCatalogReportFileEntriesDeclareToken(t *testing.T) {
	cat, err := LoadScannerCatalog()
	if err != nil {
		testutil.FailErr(t, "load bundled scanner catalog", err)
	}
	var withReportFile int
	for _, entry := range cat.Entries() {
		if CommandWantsReportFile(entry.Command) {
			withReportFile++
		}
	}
	if withReportFile == 0 {
		t.Fatal("expected at least one catalog entry to use the report-file path (gitleaks writes to a file, not stdout)")
	}
}
