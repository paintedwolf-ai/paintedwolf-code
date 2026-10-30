//go:build !darwin && !linux

package fspath

import "io/fs"

func ObserveFile(fs.FileInfo) FileObservation { return FileObservation{} }
