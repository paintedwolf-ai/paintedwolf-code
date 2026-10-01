//go:build windows

package sourcecatalog

import (
	"os"
	"syscall"
)

// Windows keeps no change time; the creation time tells a recreated directory
// from the one whose listing was recorded.
func directoryStampOf(info os.FileInfo) DirectoryStamp {
	stamp := DirectoryStamp{Modified: info.ModTime().UnixNano()}
	if attributes, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		stamp.Changed = attributes.CreationTime.Nanoseconds()
	}
	return stamp
}
