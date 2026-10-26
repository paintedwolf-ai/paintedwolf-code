//go:build !darwin && !linux

package fspath

import "io/fs"

func EntryAllocation(fs.FileInfo) Allocation { return Allocation{} }
