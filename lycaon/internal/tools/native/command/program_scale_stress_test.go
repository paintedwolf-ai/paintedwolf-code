//go:build stress

package command_test

import "testing"

func TestStressProgramEntrySessionSpillQuota(t *testing.T) {
	checkProgramEntrySpillQuota(t, 150)
}

func TestStressProgramEntryLoopRetention(t *testing.T) {
	checkProgramEntryLoopRetention(t, 12000)
}
