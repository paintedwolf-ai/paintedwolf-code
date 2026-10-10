package toolapi

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	scan "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type failedListCoordinator struct {
	scan.ScanCoordinator
	failure error
}

func (c *failedListCoordinator) List(context.Context, []string, int) ([]wire.CodeScan, error) {
	return nil, c.failure
}
func TestScanListPropagatesCoordinatorFailureWithoutInventingEmptyInventory(t *testing.T) {
	failure := errors.New("inventory unavailable")
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}}}
	result, err := runScanList(t.Context(), nil, tc, &failedListCoordinator{failure: failure})
	if result != "" || !errors.Is(err, failure) {
		t.Fatalf("inventory result=%q err=%v", result, err)
	}
}

func TestScanListRefusesCallerSuppliedWorkspaceBeforeQueryingInventory(t *testing.T) {
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}}}
	result, err := runScanList(t.Context(), map[string]any{"project_dir": t.TempDir()}, tc, nil)
	var reject *toolrejection.ToolReject
	if result != "" || !errors.As(err, &reject) || reject.Code != ScanProjectDirHostBoundCode {
		t.Fatalf("workspace substitution result=%q err=%v", result, err)
	}
}
