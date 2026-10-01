//go:build !darwin && !linux

package workspacebaseline

import "time"

// lchtimes leaves symlink timestamps alone where the platform has no call for it.
func lchtimes(string, time.Time) error { return nil }
