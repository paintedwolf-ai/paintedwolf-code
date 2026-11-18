package contract

import (
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestParseOutputImplemented(t *testing.T) {
	t.Parallel()
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, []byte(`{"results":[]}`))
	contractcheck.FailErr(t, "scan.ParseOutput failed", err)
	if res == nil {
		t.Fatal("nil result")
	}
}
