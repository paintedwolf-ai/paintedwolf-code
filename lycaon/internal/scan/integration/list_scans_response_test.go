package integration

import (
	"testing"

	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestNewListScansResponseEmpty(t *testing.T) {
	resp := scantoolapi.NewListScansResponse(t.Context(), nil)
	if resp.Count != 0 || len(resp.Scans) != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.HintCode != "SCAN_LIST_EMPTY" || resp.Hint == "" {
		t.Fatalf("empty hint = %+v", resp)
	}
}

func TestNewListScansResponsePopulated(t *testing.T) {
	resp := scantoolapi.NewListScansResponse(t.Context(), []wire.CodeScan{{ID: "s1"}})
	if resp.Count != 1 || resp.HintCode != "" {
		t.Fatalf("resp = %+v", resp)
	}
}
