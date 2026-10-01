package settingsoverlay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxFormat = 3
const FormatFileName = "overlay.yaml"

var ErrFormatTooNew = errors.New("overlay_format newer than binary")
var ErrFormatInvalid = errors.New("overlay_format invalid")

type formatDocument struct {
	OverlayFormat *int `yaml:"overlay_format"`
}

// FormatPath returns the overlay format path for root.
func FormatPath(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	return filepath.Join(Dir(filepath.Clean(root)), FormatFileName)
}

// ReadFormat returns the format for root.
func ReadFormat(root string) (int, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return MaxFormat, nil
	}
	data, err := os.ReadFile(FormatPath(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 1, nil
		}
		return 0, fmt.Errorf("read overlay format: %w", err)
	}
	return parseFormat(data)
}

func parseFormat(data []byte) (int, error) {
	var doc formatDocument
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc); err != nil {
		return 0, fmt.Errorf("%w: parse %s: %w", ErrFormatInvalid, FormatFileName, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("%w: %s must contain one YAML document", ErrFormatInvalid, FormatFileName)
	}
	if doc.OverlayFormat == nil {
		return 0, fmt.Errorf("%w: %s requires overlay_format", ErrFormatInvalid, FormatFileName)
	}
	if *doc.OverlayFormat < 1 {
		return 0, fmt.Errorf("%w: overlay_format must be >= 1", ErrFormatInvalid)
	}
	return *doc.OverlayFormat, nil
}

// CheckFormat validates one overlay format.
func CheckFormat(root string) error {
	root = strings.TrimSpace(root)
	format, err := ReadFormat(root)
	if err != nil {
		return err
	}
	if format > MaxFormat {
		return fmt.Errorf("%w: got %d max %d — upgrade the app", ErrFormatTooNew, format, MaxFormat)
	}
	if strings.TrimSpace(root) == "" {
		return nil
	}
	path := filepath.Join(Dir(root), BasenameIgnores)
	if _, err := os.Lstat(path); err == nil {
		if format < 3 {
			return fmt.Errorf("%w: %s requires overlay_format: 3 in %s; review the ignore decisions and set that marker explicitly before loading or editing them", ErrFormatInvalid, path, FormatPath(root))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect overlay artifact %s: %w", path, err)
	}
	return nil
}

// CheckFormats validates resolved overlay roots.
func CheckFormats(paths []string) error {
	for _, path := range paths {
		if path = strings.TrimSpace(path); path != "" {
			if err := CheckFormat(path); err != nil {
				return err
			}
		}
	}
	return nil
}
