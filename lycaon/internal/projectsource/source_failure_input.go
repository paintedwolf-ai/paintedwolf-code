package projectsource

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Release watcher suppression only when the failed effect left its input intact.
// Partial or uninspectable publication retains its journal scope for recovery.
func sourceFailureLeavesInput(plan *sourceMutationPlan) bool {
	if !plan.Changed {
		return true
	}
	if !plan.EffectStarted && plan.Kind != "write" && plan.Kind != "batch_write" {
		return true
	}
	switch plan.Kind {
	case "write":
		file, err := os.Open(plan.AbsPath)
		if err != nil {
			return false
		}
		defer func() { _ = file.Close() }()
		digest := sha256.New()
		n, err := io.Copy(digest, io.LimitReader(file, sourceledger.MaxRevisionContentBytes+1))
		return err == nil && n <= sourceledger.MaxRevisionContentBytes && hex.EncodeToString(digest.Sum(nil)) == plan.BaseSHA256
	case "batch_write":
		for i := range plan.Writes {
			if !sourceFailureLeavesInput(&plan.Writes[i]) {
				return false
			}
		}
		return true
	case "create", "restore":
		_, err := os.Lstat(plan.AbsPath)
		return os.IsNotExist(err)
	case "copy":
		_, err := os.Lstat(plan.ToAbs)
		return os.IsNotExist(err)
	case "rename":
		if plan.HoldStarted {
			return false
		}
		identity, err := fspath.EntryIdentity(plan.FromAbs)
		if err != nil || identity != plan.EntryIdentity {
			return false
		}
		_, err = os.Lstat(plan.ToAbs)
		return os.IsNotExist(err)
	}
	return false
}
