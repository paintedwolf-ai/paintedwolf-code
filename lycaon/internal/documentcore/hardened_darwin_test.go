package documentcore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Release builds sign the core with the hardened runtime and no entitlements,
// which forbids executing generated code. An ad-hoc signature with the same
// options enforces the same rule, so this holds on every Mac.
func TestCoreRunsUnderTheHardenedRuntime(t *testing.T) {
	built, err := Binary()
	testutil.FailErr(t, "resolve core", err)
	raw, err := os.ReadFile(built)
	testutil.FailErr(t, "read core", err)
	hardened := filepath.Join(t.TempDir(), binaryName)
	testutil.FailErr(t, "copy core", os.WriteFile(hardened, raw, 0o755))
	if out, err := exec.CommandContext(t.Context(), "codesign", "--force", "--sign", "-", "--options", "runtime", hardened).CombinedOutput(); err != nil {
		t.Fatalf("sign core with the hardened runtime: %v: %s", err, out)
	}
	out, err := exec.CommandContext(t.Context(), "codesign", "--display", "--verbose=2", hardened).CombinedOutput()
	testutil.FailErr(t, "inspect hardened signature", err)
	if !strings.Contains(string(out), "(runtime)") && !strings.Contains(string(out), ",runtime") {
		t.Fatalf("signature carries no hardened runtime: %s", out)
	}
	t.Setenv(EnvBinary, hardened)
	_, err = Verify(t.Context())
	testutil.FailErr(t, "round-trip a document under the hardened runtime", err)
}
