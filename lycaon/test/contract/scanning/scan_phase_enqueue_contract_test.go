package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/registry"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPhaseEnterSecurityRequestsBundledEngines(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	reg, err := registry.New(registry.Options{ModuleRoot: lycaonRoot})
	contractcheck.FailErr(t, "registry.New failed", err)

	projectDir := filepath.Join(root, "lycaon", "test", "testdata", "scan")
	requested := &recordedFullScanRequest{}
	run := &wire.WorkflowRun{ID: "run-ingest", SessionID: "session-ingest"}
	obligation := &scan.WorkflowObligation{
		Triggers: &scan.TriggerService{Registry: reg},
		Full:     requested,
	}
	contractcheck.FailErr(t, "OnPhaseEnter", obligation.OnPhaseEnter(context.Background(), run, projectDir, map[string]any{
		"categories": []any{"security"},
		"full":       true,
	}))

	if requested.calls != 1 || requested.projectDir != projectDir || requested.bind.WorkflowRunID != run.ID || requested.bind.SessionID != run.SessionID || requested.trigger != wire.ScanTriggerPhaseEnter {
		t.Fatalf("full scan request = %+v", requested)
	}
	if len(requested.scannerIDs) != 3 {
		t.Fatalf("requested scanners = %v, want three bundled engines", requested.scannerIDs)
	}
	got := map[string]bool{}
	for _, scannerID := range requested.scannerIDs {
		got[scannerID] = true
		_, err := scan.SelectedScannerContract(t.Context(), reg, projectDir, scannerID, wire.ScanCategorySecurity)
		contractcheck.FailErr(t, "SelectedScannerContract "+scannerID, err)
		scanner, err := reg.Get(scannerID)
		contractcheck.FailErr(t, "get scanner "+scannerID, err)
		wantEngine := map[string]wire.ScanCategory{
			"lycaon-sca":     wire.ScanCategorySCA,
			"lycaon-secrets": wire.ScanCategorySecret,
			"lycaon-sast":    wire.ScanCategorySAST,
		}[scannerID]
		if !scanHasCategory(scanner.Categories(), wantEngine) || !scanHasCategory(scanner.Categories(), wire.ScanCategorySecurity) {
			t.Fatalf("scan %s categories = %v, want %s and security", scannerID, scanner.Categories(), wantEngine)
		}
	}
	for _, want := range []string{"lycaon-sca", "lycaon-secrets", "lycaon-sast"} {
		if !got[want] {
			t.Fatalf("missing request for scanner %q", want)
		}
	}
}

func scanHasCategory(have []wire.ScanCategory, want wire.ScanCategory) bool {
	for _, c := range have {
		if c == want {
			return true
		}
	}
	return false
}

type recordedFullScanRequest struct {
	calls      int
	projectDir string
	scannerIDs []string
	trigger    wire.ScanTrigger
	bind       scan.FullScanContext
}

func (r *recordedFullScanRequest) RequestFull(_ context.Context, projectDir string, scannerIDs []string, trigger wire.ScanTrigger, bind scan.FullScanContext) (scan.FullPass, error) {
	r.calls++
	r.projectDir, r.scannerIDs, r.trigger, r.bind = projectDir, append([]string(nil), scannerIDs...), trigger, bind
	return scan.FullPass{}, nil
}
