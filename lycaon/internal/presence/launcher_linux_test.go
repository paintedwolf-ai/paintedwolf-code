package presence

import (
	"errors"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxLauncherNeedsTheCustodianVerdictForThisParent(t *testing.T) {
	if err := VerifyLauncher(LauncherEvidence{}); !errors.Is(err, ErrLauncherUnverified) {
		t.Fatalf("no verdict: %v", err)
	}
	if err := VerifyLauncher(LauncherEvidence{CustodianVerifiedParentPID: os.Getppid() + 1}); !errors.Is(err, ErrLauncherUnverified) {
		t.Fatalf("another parent: %v", err)
	}
	testutil.FailErr(t, "verify this parent", VerifyLauncher(LauncherEvidence{CustodianVerifiedParentPID: os.Getppid()}))
}
