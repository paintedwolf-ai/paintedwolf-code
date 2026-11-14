//go:build !darwin && !linux

package adapter

import "io/fs"

// Without a change time or inode only size and mtime speak, and the racy rule still applies.
func statIdentityOf(fs.FileInfo) statIdentity { return statIdentity{} }
