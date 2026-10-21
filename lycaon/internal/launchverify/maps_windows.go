//go:build windows

package launchverify

import (
	"errors"
	"io/fs"
)

func lstatFacts(string) (fileFacts, error) {
	return fileFacts{}, errors.New("process mappings are verified on Linux only")
}

func factsOf(fs.FileInfo) (fileFacts, error) {
	return fileFacts{}, errors.New("process mappings are verified on Linux only")
}
