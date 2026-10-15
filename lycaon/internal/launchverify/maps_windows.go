//go:build windows

package launchverify

import "errors"

func lstatFacts(string) (fileFacts, error) {
	return fileFacts{}, errors.New("process mappings are verified on Linux only")
}
