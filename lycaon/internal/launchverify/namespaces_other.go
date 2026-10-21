//go:build !linux

package launchverify

import "errors"

func ReadNamespace(string, int) (NamespaceFacts, error) {
	return NamespaceFacts{}, errors.New("protected user namespace verification requires Linux")
}
