//go:build !darwin && !linux

package workspace

import "io/fs"

func sourceFileIdentity(_ fs.FileInfo) fileIdentity {
	return fileIdentity{}
}
