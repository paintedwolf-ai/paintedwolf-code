//go:build !darwin && !linux

package literal

import "os"

// statVolume is nil where no file system statistics owner exists; df then
// refuses as platform unavailable.
var statVolume func(*os.File, string) (volume, error)
