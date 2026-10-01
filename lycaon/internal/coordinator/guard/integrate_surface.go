package guard

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
)

const (
	workerCancelTerminalCode   = "WORKER_CANCEL_TERMINAL"
	overlayPromoteNotFoundCode = "OVERLAY_PROMOTE_NOT_FOUND"
)

// OverlayIntegratePending reports whether write overlays still await promote or reject.
func OverlayIntegratePending(implState surface.ImplementSessionState) bool {
	return len(implState.PendingOverlayIDs) > 0
}

// OverlayIntegrateRejectEndsToolLoop reports whether an overlay-tool reject with an
// empty pending ledger means integrate is done and the host should reconcile.
func OverlayIntegrateRejectEndsToolLoop(rejectCode string, implState surface.ImplementSessionState) bool {
	if OverlayIntegratePending(implState) {
		return false
	}
	switch rejectCode {
	case workerCancelTerminalCode, overlayPromoteNotFoundCode:
		return true
	default:
		return false
	}
}
