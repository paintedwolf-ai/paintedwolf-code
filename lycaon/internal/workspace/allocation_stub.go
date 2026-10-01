//go:build !darwin && !linux

package workspace

import "io/fs"

func allocatedFileBytes(info fs.FileInfo) int64 {
	return info.Size()
}
