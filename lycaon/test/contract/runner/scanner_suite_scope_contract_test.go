package contract

// The scanner suite resolves its bundled engine before running.

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const scannerSuiteTask = "test:scanners"

func TestScannerSuiteStagesItsEngine(t *testing.T) {
	t.Parallel()
	body := taskBody(taskfile(t), scannerSuiteTask)
	if body == "" {
		t.Fatalf("Taskfile.yml has no %s task", scannerSuiteTask)
	}
	if !strings.Contains(body, "with-bundled-scanners.sh") ||
		!strings.Contains(contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/with-bundled-scanners.sh"), "resolve-opengrep.sh") {
		t.Fatalf("%s must resolve its bundled engine before running", scannerSuiteTask)
	}
}
