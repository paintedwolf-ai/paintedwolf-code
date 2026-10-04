//go:build !darwin && !linux

package preflight

import "errors"

// The disk probe treats this error as "no evidence", never as a failure.
var errNoHostFacts = errors.New("preflight: host facts are unavailable on this platform")

var osProductVersion func() (string, error)

func freeBytes(string) (uint64, error) { return 0, errNoHostFacts }
