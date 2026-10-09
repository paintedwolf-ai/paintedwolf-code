package projectsource

import (
	"path/filepath"
	"runtime"
)

// A completed native move needs its acknowledged location before recovery can promise Undo.
func nativeTrashReceiptRecorded(plan *sourceMutationPlan) bool {
	receipt := plan.NativeTrash.Receipt
	return receipt.Platform == runtime.GOOS && filepath.IsAbs(receipt.Path) && receipt.Identity != ""
}
