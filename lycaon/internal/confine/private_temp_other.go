//go:build !linux

package confine

// PrivateTempFor is empty on platforms without private temp mounts.
func PrivateTempFor(string) PrivateTemp { return PrivateTemp{} }
