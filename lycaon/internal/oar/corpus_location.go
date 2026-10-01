package oar

import (
	"os"
	"path/filepath"
)

func FindCorpusDir(start string) string {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	dir := start
	for {
		candidate := filepath.Join(dir, "schemas", "oar", "conformance")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
