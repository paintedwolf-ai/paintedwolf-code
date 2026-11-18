//go:build !darwin && !linux

package modelstore

import (
	"errors"
	"os"
)

var errUnsupportedPlatform = errors.New("modelstore: local generation requires macOS or Linux")

func lockFile(*os.File) error                { return errUnsupportedPlatform }
func defaultFreeBytes(string) (int64, error) { return 0, errUnsupportedPlatform }

func openBundlePayload(*os.Root, string) (*os.File, error) {
	return nil, errUnsupportedPlatform
}

func openPartial(*os.Root, string) (*os.File, error) { return nil, errUnsupportedPlatform }

func reclaimableBlocks(os.FileInfo, int) int64 { return 0 }
