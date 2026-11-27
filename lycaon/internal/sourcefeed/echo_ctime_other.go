//go:build !darwin && !linux

package sourcefeed

import "os"

func statChangeTime(os.FileInfo) ([2]int64, bool) {
	return [2]int64{}, false
}
