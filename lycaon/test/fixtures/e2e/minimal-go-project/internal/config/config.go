package config

import (
	"fmt"
	"os"
	"strings"
)

// Config contains values required to construct the service.
type Config struct {
	ListenAddr string
}

// LoadConfig reads environment settings and validates them before startup.
func LoadConfig() (Config, error) {
	addr := strings.TrimSpace(os.Getenv("FIXTURE_LISTEN_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	if !strings.Contains(addr, ":") {
		return Config{}, fmt.Errorf("listen address %q must include a port", addr)
	}
	return Config{ListenAddr: addr}, nil
}
