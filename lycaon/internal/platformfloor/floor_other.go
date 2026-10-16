//go:build !darwin && !linux

package platformfloor

// HostOSMin is empty where no desktop floor is defined.
func HostOSMin() string { return "" }
