//go:build !darwin && !linux

package modelstore

import (
	"errors"
	"os"
)

var errUnsupportedPlatform = errors.New("modelstore: local generation requires macOS or Linux")

func lockFile(*os.File) error                { return errUnsupportedPlatform }
func defaultFreeBytes(string) (int64, error) { return 0, errUnsupportedPlatform }
