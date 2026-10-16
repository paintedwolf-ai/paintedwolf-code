package presence

import (
	"fmt"
	"os"
)

// VerifyLauncher confirms that the process that started this engine is the
// installed desktop app. The engine cannot read its parent's image once the
// shell is non-dumpable, so the proof is the identity service's: it opened
// the parent's image through the process filesystem with its own privilege,
// compared it with the installed manifest, and named the parent it checked.
// The engine confirms it still has that parent.
func VerifyLauncher(evidence LauncherEvidence) error {
	if evidence.CustodianVerifiedParentPID <= 0 {
		return fmt.Errorf("%w: the vault identity service did not verify the launching process", ErrLauncherUnverified)
	}
	if parent := os.Getppid(); parent != evidence.CustodianVerifiedParentPID {
		return fmt.Errorf("%w: the launching process changed since verification (pid %d, verified %d)",
			ErrLauncherUnverified, parent, evidence.CustodianVerifiedParentPID)
	}
	return nil
}
