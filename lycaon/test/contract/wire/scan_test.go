package contract

import (
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestScannerConfigLoads(t *testing.T) {
	t.Parallel()
	cfg, err := scancatalog.LoadScannerConfig()
	contractcheck.FailErr(t, "scan.LoadScannerConfig failed", err)
	if len(cfg.Scanners) < 3 {
		t.Fatalf("expected >=3 scanners, got %d", len(cfg.Scanners))
	}
	for _, s := range cfg.Scanners {
		if s.ID == "" || len(s.Categories) == 0 {
			t.Fatalf("invalid scanner entry: %+v", s)
		}
		if s.Driver == "" {
			t.Fatalf("driver required: %+v", s)
		}
		if s.Driver != scancatalog.DriverExternal && s.Impl == "" {
			t.Fatalf("driver without impl: %+v", s)
		}
	}
}

func TestParseOutputImplemented(t *testing.T) {
	t.Parallel()
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, []byte(`{"results":[]}`))
	contractcheck.FailErr(t, "scan.ParseOutput failed", err)
	if res == nil {
		t.Fatal("nil result")
	}
}
