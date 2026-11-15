//go:build !darwin && !linux

package privategit

import (
	"io/fs"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func fillLowerIdentity(*layeredfs.LowerEntry, fs.FileInfo) {}
