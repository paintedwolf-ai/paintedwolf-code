//go:build stress && !unix

package db_test

import "io/fs"

// Platforms without Unix block accounting report logical file lengths.
func yearAllocatedBytes(info fs.FileInfo) int64 { return info.Size() }
