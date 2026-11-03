//go:build !windows

package conformance_test

import "syscall"

// Match the reference driver's file-creation environment for this test process.
func setReferenceUmask() { syscall.Umask(0o022) }
