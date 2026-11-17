package toolapi

import (
	"context"
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestListScansResponseHintsOnlyWhenEmpty(t *testing.T) {
	empty := NewListScansResponse(context.Background(), nil)
	if empty.HintCode != "SCAN_LIST_EMPTY" || empty.Count != 0 || empty.Scans == nil || len(empty.Scans) != 0 {
		t.Fatalf("empty list = %+v, want SCAN_LIST_EMPTY over an empty scan list", empty)
	}
	if strings.TrimSpace(empty.Hint) == "" || strings.Contains(empty.Hint, "{{") {
		t.Fatalf("empty list hint = %q, want rendered registry copy", empty.Hint)
	}
	scans := []wire.CodeScan{{ID: "scan-1"}, {ID: "scan-2"}}
	listed := NewListScansResponse(context.Background(), scans)
	if listed.HintCode != "" || listed.Hint != "" || listed.Count != 2 || len(listed.Scans) != 2 {
		t.Fatalf("listed scans = %+v, want two scans and no hint", listed)
	}
}
