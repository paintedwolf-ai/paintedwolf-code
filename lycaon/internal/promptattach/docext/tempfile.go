package docext

import (
	"os"

	"github.com/lycaon/lycaon/internal/promptattach/docformat"
)

// writeTemp stages bytes under a format-specific suffix.
func writeTemp(dir string, format docformat.Format, raw []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp(dir, "attachment-*"+docformat.Ext(format))
	if err != nil {
		return "", nil, err
	}
	path = f.Name()
	cleanup = func() { _ = os.Remove(path) }
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
