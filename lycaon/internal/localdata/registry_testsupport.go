package localdata

import (
	"os"
	"path/filepath"
)

// SeedDurableMarkers creates fixtures for every protected path under base.
func SeedDurableMarkers(base string) error {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	for _, p := range DurableAbsPaths(base) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte("keep\n"), 0o600); err != nil {
			return err
		}
	}
	for _, rel := range durableRelDirs {
		dir := filepath.Join(base, rel)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "durable-marker.json"), []byte("keep\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}
