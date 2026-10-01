package authzcontext

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configlayout"
)

// DefaultAuditConfig returns production defaults.
func DefaultAuditConfig() AuditConfig {
	return AuditConfig{CaptureRawArgv: false}
}

// AuditConfig controls authorization event detail.
type AuditConfig struct {
	CaptureRawArgv bool
}

type auditFile struct {
	Audit struct {
		CaptureRawArgv bool `yaml:"capture_raw_argv"`
	} `yaml:"audit"`
}

// LoadAuditConfig loads the audit catalog and its checkout override.
func LoadAuditConfig(moduleRoot string) (AuditConfig, error) {
	cfg := DefaultAuditConfig()
	raw, err := config.Read(config.Audit)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}
	if err == nil {
		parsed, perr := parseAuditFile(raw)
		if perr != nil {
			return cfg, perr
		}
		cfg = parsed
	}
	if !configlayout.IsModuleRoot(moduleRoot) {
		return cfg, nil
	}
	path := filepath.Join(strings.TrimSpace(moduleRoot), "config", "packs", "painted-wolf", "security", "host", "audit.yaml")
	disk, derr := os.ReadFile(path) // #nosec G304 — checkout path gated by IsModuleRoot
	if derr != nil {
		if os.IsNotExist(derr) {
			return cfg, nil
		}
		return cfg, derr
	}
	return parseAuditFile(disk)
}

func parseAuditFile(raw []byte) (AuditConfig, error) {
	cfg := DefaultAuditConfig()
	var file auditFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return cfg, err
	}
	cfg.CaptureRawArgv = file.Audit.CaptureRawArgv
	return cfg, nil
}
