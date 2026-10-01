package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLycaonRulesScript(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped under -short")
	}
	root := testutil.CheckoutRoot(t)
	script := filepath.Join(root, "scripts", "test-lycaon-rules.sh")
	if _, err := os.Stat(script); err != nil {
		testutil.FailErr(t, "stat path", err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test-lycaon-rules.sh failed: %v\n%s", err, out)
	}
}
