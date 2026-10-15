//go:build darwin

package platformfloor

// HostOSMin is the version floor the running host compares itself against.
func HostOSMin() string { return MacOSMin() }
