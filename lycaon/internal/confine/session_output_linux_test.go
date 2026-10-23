//go:build linux

package confine_test

import "testing"

func TestSessionOutputLandlock(t *testing.T) {
	sessionOutputKernelProof(t, helperBinary(t, requireLinuxSandbox(t)))
}
