//go:build !darwin && !linux && !windows

package fspath

import (
	"fmt"
	"os"
)

func EntryIdentity(string) (string, error) { return "", fmt.Errorf("filesystem identity unavailable") }

func SameFilesystem(string, string) (bool, error) {
	return false, fmt.Errorf("filesystem identity unavailable")
}

func identityWithoutDevice(identity string) string { return identity }

func FileIdentity(*os.File) (string, error) {
	return "", fmt.Errorf("filesystem identity unavailable")
}
