//go:build !darwin && !linux

package fseffect

import "os"

// VolumeOf has no file system statistics owner on this platform.
func VolumeOf(*os.File, string) (Volume, error) { return Volume{}, ErrUnsupported }
