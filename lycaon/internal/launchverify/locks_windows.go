//go:build windows

package launchverify

import "os"

func sharedFlockHeld(_ *os.File, _ string, _ int, _ fileFacts) bool {
	return false
}
