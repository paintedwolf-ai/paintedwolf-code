//go:build !darwin

package preflight

import "errors"

// The probe set is macOS-shaped; these stubs keep the package building on Linux
// CI. Probes treat an error here as "no evidence", never as a failure.

var errNotDarwin = errors.New("preflight: host facts are macOS-only")

func osProductVersion() (string, error) { return "", errNotDarwin }

func freeBytes(string) (uint64, error) { return 0, errNotDarwin }
