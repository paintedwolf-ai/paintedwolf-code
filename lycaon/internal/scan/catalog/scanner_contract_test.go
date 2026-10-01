package catalog_test

import (
	"os"
	"path/filepath"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testScannerContract(id string) scancatalog.ScannerContract {
	return scancatalog.ScannerContract{
		ScannerID:             id,
		Engine:                "test-engine",
		Driver:                scancatalog.DriverLibrary,
		Scope:                 scancatalog.ScopeSourceDriver,
		DefinitionFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func TestScannerContractUsesStructuredDefinitionFacts(t *testing.T) {
	entry := scancatalog.ScannerEntry{
		ID: "name-without-class", Driver: scancatalog.DriverExternal, Engine: "engine-x",
		ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
		Command: []string{"engine-x", "scan", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF,
	}
	contract := entry.Contract()
	if contract.Engine != "engine-x" || contract.Scope != scancatalog.ScopeDependencies || !contract.Valid() {
		t.Fatalf("contract = %+v", contract)
	}
	changed := entry
	changed.Command = []string{"engine-x", "scan", "--locked"}
	if changed.Contract().DefinitionFingerprint == contract.DefinitionFingerprint {
		t.Fatal("definition fingerprint did not move with argv")
	}
}

func TestScannerContractRejectsMalformedFingerprint(t *testing.T) {
	contract := testScannerContract("scanner")
	contract.DefinitionFingerprint = "not-a-fingerprint"
	if contract.Valid() {
		t.Fatal("malformed fingerprint must fail")
	}
}

func TestExecutionManifestTracksExternalExecutableBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scanner")
	testutil.FailErr(t, "write scanner v1", os.WriteFile(path, []byte("scanner-v1"), 0o755))
	contract := testScannerContract("external")
	contract.Driver = scancatalog.DriverExternal
	contract.EngineExecutable = path
	first, firstFingerprint, err := scancatalog.ExecutionManifest(contract)
	testutil.FailErr(t, "ExecutionManifest v1", err)

	testutil.FailErr(t, "write scanner v2", os.WriteFile(path, []byte("scanner-v2"), 0o755))
	second, secondFingerprint, err := scancatalog.ExecutionManifest(contract)
	testutil.FailErr(t, "ExecutionManifest v2", err)
	if first.EngineSHA256 == second.EngineSHA256 || firstFingerprint == secondFingerprint {
		t.Fatal("execution identity did not move with external executable bytes")
	}
}

func TestExecutionManifestTracksLibraryEngineBytes(t *testing.T) {
	manifest, _, err := scancatalog.ExecutionManifest(testScannerContract("library"))
	testutil.FailErr(t, "ExecutionManifest", err)
	if manifest.EngineSHA256 == "" {
		t.Fatal("library execution manifest omitted running engine bytes")
	}
}
