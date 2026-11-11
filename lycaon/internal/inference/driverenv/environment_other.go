//go:build !linux

package driverenv

// Prepare leaves native platform driver selection unchanged.
func Prepare(string) ([]string, func(), error) { return nil, func() {}, nil }
