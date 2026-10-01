//go:build !darwin || !cgo

package systemproxy

import "net/url"

// Lookup reports no available system proxy.
func Lookup(*url.URL) (*url.URL, error) { return nil, nil }
