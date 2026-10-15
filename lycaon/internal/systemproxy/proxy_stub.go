//go:build (!darwin && !linux) || (darwin && !cgo)

package systemproxy

import "net/url"

// Current reports no system proxy configuration.
func Current() (Configuration, error) { return Configuration{Source: SourceNone, Mode: ModeNone}, nil }

// Lookup reports no available system proxy.
func Lookup(*url.URL) (*url.URL, error) { return nil, nil }
