//go:build !darwin && !linux

package confine

import "io/fs"

func exclusionHasAliases(fs.FileInfo) bool { return true }
