package board

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

// PackContentHash computes a stable hash for inject dedup from snapshot inputs.
func PackContentHash(snap api.BoardSnapshot, now time.Time) string {
	wfRun := ""
	wfPhase := ""
	if snap.ActiveWorkflowRun != nil {
		wfRun = snap.ActiveWorkflowRun.ID
		wfPhase = snap.ActiveWorkflowRun.CurrentPhase
	}
	payload := strings.Join([]string{
		packboard.OrientationFingerprint(snap),
		packboard.PulseFingerprint(snap, now),
		wfRun,
		wfPhase,
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:16])
}
