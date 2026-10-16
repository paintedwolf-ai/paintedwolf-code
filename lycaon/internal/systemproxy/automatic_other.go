//go:build !darwin || !cgo

package systemproxy

import "net/url"

func evaluateMacOSAutomatic(*url.URL) (*url.URL, error) {
	return nil, &Unsupported{Source: SourceMacOS, Mechanism: "proxy auto-configuration"}
}
