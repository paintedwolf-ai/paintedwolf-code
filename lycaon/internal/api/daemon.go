package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
)

const daemonManifestMode = 0o600

// DaemonManifest contains discoverable process metadata without credentials.
type DaemonManifest struct {
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// WriteDaemonManifest stores process metadata with owner-only permissions.
func WriteDaemonManifest(host string, port int, pid int) error {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return err
	}
	m := DaemonManifest{
		Host:      host,
		Port:      port,
		PID:       pid,
		StartedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeOwnerOnlyFile(filepath.Join(dir, "daemon.json"), data)
}

// WriteAPITokenFile persists the bearer token for CLI attachment with owner-only permissions.
func WriteAPITokenFile(token string) (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "api.token")
	if err := writeOwnerOnlyFile(path, []byte(token)); err != nil {
		return "", err
	}
	return path, nil
}

// ReadAPITokenFile loads the token written by WriteAPITokenFile.
func ReadAPITokenFile() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "api.token"))
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(data))
	if t == "" {
		return "", fmt.Errorf("empty api token file")
	}
	return t, nil
}

// ReadDaemonManifest loads stored process metadata.
func ReadDaemonManifest() (DaemonManifest, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return DaemonManifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if err != nil {
		return DaemonManifest{}, err
	}
	var m DaemonManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return DaemonManifest{}, fmt.Errorf("parse daemon manifest: %w", err)
	}
	return m, nil
}
