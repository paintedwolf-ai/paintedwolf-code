//go:build gts_recovery_telemetry

package gotreesitter

import "time"

type recoveryRuntimeDetailedState struct {
	attempts       RecoveryRuntimeAttempts
	byTree         map[*Tree]int
	activeStarted  time.Time
	activeHeap     uint64
	activeTotal    uint64
	activeMallocs  uint64
	activeCondense uint64
}
