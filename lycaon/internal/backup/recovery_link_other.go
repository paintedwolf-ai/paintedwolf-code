//go:build !linux

package backup

import "os"

func immutableRecoveryBody(string, os.FileInfo) bool        { return false }
func linkSnapshotBody(archiveSource, string) (bool, error)  { return false, nil }
func sharedRecoveryBodyOnVolume(string, archiveSource) bool { return false }
