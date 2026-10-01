// Package diagnostics assembles redacted local diagnostics bundles.
package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/observability"
)

// maxLogTailBytes bounds each included log tail.
const maxLogTailBytes = 256 * 1024

// Input is the facts the caller already gathered.
type Input struct {
	// Health is the GET /health payload.
	Health any
	// Preflight is the GET /v1/preflight payload.
	Preflight any
	// ConfigDir is the user config root whose YAML is included, scrubbed.
	ConfigDir string
	// LogDir holds recent log files; only the tail of each is read.
	LogDir string
	// GeneratedAt stamps the manifest.
	GeneratedAt time.Time
	// Redactor adds catalog-backed detection. Nil uses baseline scrubbing.
	Redactor *Redactor
}

// BuildStartup assembles a store-free startup bundle.
func BuildStartup(ctx context.Context, configDir string, generatedAt time.Time) ([]byte, error) {
	redactor, err := NewRedactor()
	if err != nil {
		return nil, err
	}
	return Build(ctx, Input{
		Health: map[string]any{
			"status": "unavailable",
			"reason": "engine_not_started",
		},
		Preflight: map[string]any{
			"overall": "unavailable",
			"reason":  "engine_not_started",
			"probes":  []any{},
		},
		ConfigDir:   configDir,
		LogDir:      configDir,
		GeneratedAt: generatedAt,
		Redactor:    redactor,
	})
}

// Build assembles the bundle and returns the zip bytes.
func Build(ctx context.Context, in Input) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifest := map[string]any{
		"generated_at": in.GeneratedAt.UTC().Format(time.RFC3339),
		"contents":     []string{"health.json", "preflight.json", "boundary-overrides.json", "config/", "logs/"},
		"redaction":    "secrets removed by layered diagnostics scrubber; secret stores excluded entirely",
		"upload":       "none — this file is saved locally and shared only if you choose to",
	}
	if err := writeJSONEntry(ctx, zw, "manifest.json", manifest, in.Redactor); err != nil {
		return nil, err
	}
	if err := writeJSONEntry(ctx, zw, "boundary-overrides.json", confine.CurrentEnvironmentOverrides(), in.Redactor); err != nil {
		return nil, err
	}
	if err := writeJSONEntry(ctx, zw, "health.json", in.Health, in.Redactor); err != nil {
		return nil, err
	}
	if err := writeJSONEntry(ctx, zw, "preflight.json", in.Preflight, in.Redactor); err != nil {
		return nil, err
	}
	if err := writeConfigEntries(ctx, zw, in.ConfigDir, in.Redactor); err != nil {
		return nil, err
	}
	if err := writeLogEntries(ctx, zw, in.LogDir, in.Redactor); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("diagnostics: close zip: %w", err)
	}
	return buf.Bytes(), nil
}

// writeJSONEntry marshals v and scrubs it before it reaches the archive.
func writeJSONEntry(ctx context.Context, zw *zip.Writer, name string, v any, redactor *Redactor) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("diagnostics: marshal %s: %w", name, err)
	}
	return writeEntry(zw, name, []byte(redactText(ctx, redactor, string(observability.ScrubJSON(raw)))))
}

func writeEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("diagnostics: create %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("diagnostics: write %s: %w", name, err)
	}
	return nil
}

func writeConfigEntries(ctx context.Context, zw *zip.Writer, configDir string, redactor *Redactor) error {
	if strings.TrimSpace(configDir) == "" {
		return nil
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		// Preflight reports missing configuration directories.
		return nil //nolint:nilerr // absence is reported by preflight, not here
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !localdata.IsDiagnosticsAllowed(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			continue
		}
		if err := writeEntry(zw, "config/"+name, []byte(redactText(ctx, redactor, string(raw)))); err != nil {
			return err
		}
	}
	return nil
}

func writeLogEntries(ctx context.Context, zw *zip.Writer, logDir string, redactor *Redactor) error {
	if strings.TrimSpace(logDir) == "" {
		return nil
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil //nolint:nilerr // no logs is not a bundle failure
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isLogFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		tail, err := readTail(filepath.Join(logDir, name), maxLogTailBytes)
		if err != nil {
			continue
		}
		if err := writeEntry(zw, "logs/"+name, []byte(redactText(ctx, redactor, string(tail)))); err != nil {
			return err
		}
	}
	return nil
}

func redactText(ctx context.Context, redactor *Redactor, text string) string {
	if redactor != nil {
		return redactor.RedactText(ctx, text)
	}
	return observability.RedactString(text)
}

func isLogFile(name string) bool {
	return strings.HasSuffix(name, ".log") ||
		strings.HasSuffix(name, ".log.1") ||
		strings.HasSuffix(name, ".jsonl") ||
		strings.HasSuffix(name, ".jsonl.1")
}

// readTail returns at most max trailing bytes of path.
func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > max {
		if _, err := f.Seek(info.Size()-max, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}
