//go:build windows

package bundleddriver

import "os"

func checkSingleLink(string, os.FileInfo) error { return nil }
