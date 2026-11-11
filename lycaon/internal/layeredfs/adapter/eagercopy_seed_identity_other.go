//go:build !darwin && !linux

package adapter

import "io/fs"

// sharedFileID reports no shared identity where the platform does not expose one.
func sharedFileID(fs.FileInfo) (seedFileID, bool) { return seedFileID{}, false }
