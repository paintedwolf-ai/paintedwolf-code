package confine

import (
	"strconv"
	"strings"
)

// ListenPortsLabel renders a port set for logs and grant cards.
func ListenPortsLabel(ports []uint16) string {
	if len(ports) == 0 {
		return "any local port"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(int(p)))
	}
	return "local port " + strings.Join(parts, ", ")
}
