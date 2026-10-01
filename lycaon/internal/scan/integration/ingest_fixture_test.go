package integration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
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

func completeIngestMeta(t *testing.T, meta scan.IngestMeta) scan.IngestMeta {
	t.Helper()
	manifest, fingerprint, err := scancatalog.ExecutionManifest(meta.Scanner)
	testutil.FailErr(t, "ExecutionManifest", err)
	meta.CoverageStatus = api.ScanCoverageComplete
	meta.ExecutionManifest = &manifest
	meta.ExecutionFingerprint = fingerprint
	return meta
}
