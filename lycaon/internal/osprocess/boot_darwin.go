//go:build darwin

package osprocess

import "golang.org/x/sys/unix"

// BootID identifies this kernel boot. Process and mount identities recorded under
// another boot cannot name anything alive now.
func BootID() (string, bool) {
	id, err := unix.Sysctl("kern.bootsessionuuid")
	return id, err == nil && id != ""
}
