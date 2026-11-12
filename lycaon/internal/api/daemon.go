package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

const (
	daemonManifestMode = 0o600
	// DaemonTokenKey is the record key inside NamespaceDaemonToken.
	DaemonTokenKey = "bearer"
	// LegacyTokenFileName is the retired plaintext bearer token filename.
	LegacyTokenFileName = "api.token"
)

// DefaultDaemonTokenSlot returns the vault slot storing the standalone daemon bearer token.
func DefaultDaemonTokenSlot() (credentialstore.Slot, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return daemonTokenSlot(dir), nil
}

func daemonTokenSlot(dir string) credentialstore.Slot {
	return credentialstore.Slot{
		Path:      credentialstore.DefaultVaultPath(dir),
		Namespace: credentialstore.NamespaceDaemonToken,
		Context:   "daemon bearer token",
	}
}

func validateDaemonTokenID(id string) bool {
	return id == DaemonTokenKey
}

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

// WriteAPIToken persists the standalone daemon bearer token in the encrypted
// credential vault under ("daemon-token", "bearer") and unlinks any legacy
// plaintext api.token only after durable vault verification succeeds.
func WriteAPIToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("empty api token")
	}
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	slot := daemonTokenSlot(dir)
	store, err := credentialstore.Open(slot, validateDaemonTokenID)
	if err != nil {
		return "", fmt.Errorf("open daemon token vault: %w", err)
	}
	if err := store.Set(DaemonTokenKey, token); err != nil {
		return "", fmt.Errorf("write daemon token to vault: %w", err)
	}
	verified, err := credentialstore.Open(slot, validateDaemonTokenID)
	if err != nil {
		return "", fmt.Errorf("verify daemon token vault: %w", err)
	}
	if got, ok := verified.Get(DaemonTokenKey); !ok || got.Value() != token {
		return "", errors.New("verify daemon token vault: persisted value mismatch")
	}
	legacyPath := filepath.Join(dir, LegacyTokenFileName)
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return slot.Path, fmt.Errorf("remove legacy %s after vault persistence: %w", legacyPath, err)
	}
	return slot.Path, nil
}

// ReadAPIToken loads the standalone daemon bearer token from the encrypted
// credential vault under ("daemon-token", "bearer") without mutating storage
// or falling back to legacy plaintext files.
func ReadAPIToken() (string, error) {
	dir, err := configdir.Root()
	if err != nil {
		return "", err
	}
	slot := daemonTokenSlot(dir)
	if _, err := os.Stat(slot.Path); err != nil {
		return "", fmt.Errorf("read daemon token vault: %w", err)
	}
	store, err := credentialstore.Open(slot, validateDaemonTokenID)
	if err != nil {
		return "", fmt.Errorf("open daemon token vault: %w", err)
	}
	got, ok := store.Get(DaemonTokenKey)
	if !ok {
		return "", fmt.Errorf("daemon token not found in %s: %w", slot.Path, os.ErrNotExist)
	}
	t := strings.TrimSpace(got.Value())
	if t == "" {
		return "", errors.New("empty daemon token in vault")
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
