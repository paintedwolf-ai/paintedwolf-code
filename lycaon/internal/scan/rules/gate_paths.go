package rules

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// MaterializeGateRules writes the immutable bundle inside the scanner's private output root.
func MaterializeGateRules(cfg *OpengrepGatesConfig, moduleRoot, outputDir string) ([]string, error) {
	bundle, err := CompileGateRules(cfg, moduleRoot)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(outputDir) {
		return nil, fmt.Errorf("absolute scanner output directory required")
	}
	digest := sha256.Sum256(bundle)
	path := filepath.Join(outputDir, fmt.Sprintf("%x.yaml", digest))
	if err := spillFile(path, bundle); err != nil {
		return nil, err
	}
	return []string{path}, nil
}

func spillFile(dest string, data []byte) error {
	if current, err := os.ReadFile(dest); err == nil && bytes.Equal(current, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(dest), Source: bytes.NewReader(data), Mode: 0o600, DirMode: 0o750,
	})
	return err
}
