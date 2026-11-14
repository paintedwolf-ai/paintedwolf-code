//go:build linux

package osprocess

import (
	"os"
	"strings"
)

// BootID identifies this kernel boot. Process and mount identities recorded under
// another boot cannot name anything alive now.
func BootID() (string, bool) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(raw))
	return id, id != ""
}
