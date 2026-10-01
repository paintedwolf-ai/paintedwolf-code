package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanRetainsTargetFailureAlongsideUsableFindings(t *testing.T) {
	skipIfOpenGrepUnavailable(t)
	root := t.TempDir()
	// Keep a rule candidate so the engine parses the malformed target.
	broken := []byte("package broken\nimport \"crypto/tls\"\nfunc bad() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }\nfunc broken( {\n")
	testutil.FailErr(t, "write invalid source fixture", os.WriteFile(filepath.Join(root, "broken.go"), broken, 0o600))
	vulnerable, err := os.ReadFile(filepath.Join(scanFixtureDir(t), "vuln.go"))
	testutil.FailErr(t, "read valid finding fixture", err)
	// A large comment crosses the engine's default file-size filter without adding syntax complexity.
	padding := "/*" + strings.Repeat(" ", 2<<20) + "*/\n"
	vulnerable = append([]byte(padding), vulnerable...)
	testutil.FailErr(t, "write valid finding fixture", os.WriteFile(filepath.Join(root, "vuln.go"), vulnerable, 0o600))
	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	cancel := h.StartBackgroundWorkers(t, t.Context())
	t.Cleanup(cancel)
	created := enqueueCodeScan(t, h.Server, root, []wire.ScanCategory{wire.ScanCategorySAST})
	got := waitScanComplete(t, h.Server, created.ProjectID, created.ID, bundledScannerWaitBudget)
	assertScanFindings(t, got, "vuln.go", 1)
	if got.CoverageStatus != wire.ScanCoveragePartial {
		t.Fatalf("invalid source claimed %s coverage: %+v", got.CoverageStatus, got.Warnings)
	}
	diagnosed := false
	for _, warning := range got.Warnings {
		if warning.File == "broken.go" {
			diagnosed = true
		}
	}
	if !diagnosed {
		t.Fatalf("target failure lost its path: %+v", got.Warnings)
	}
	after, err := os.ReadFile(filepath.Join(root, "broken.go"))
	testutil.FailErr(t, "read original target", err)
	if string(after) != string(broken) {
		t.Fatal("scan altered invalid source bytes")
	}
}
