//go:build !linux

package confine

import "os"

// supervisedRefusalReports is false where the kernel's own stream, when there
// is one, carries refusals.
func supervisedRefusalReports() bool { return false }

// refusalReport has no pipe where the kernel's own stream carries refusals.
func refusalReport(Confinement) (*os.File, func()) { return nil, func() {} }
