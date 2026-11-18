package db

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// closedStores remembers store files this process opened, verified and
// cleanly closed, with the file identity they were left in.
var closedStores = closedStoreFiles{files: map[string]storeFileStamp{}}

type storeFileStamp struct {
	size    int64
	modTime time.Time
}

type closedStoreFiles struct {
	mu    sync.Mutex
	files map[string]storeFileStamp
}

func (c *closedStoreFiles) record(path string) {
	stamp, ok := quiescentStamp(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok {
		c.files[path] = stamp
	} else {
		delete(c.files, path)
	}
}

func (c *closedStoreFiles) unchanged(path string) bool {
	stamp, ok := quiescentStamp(path)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	recorded, found := c.files[path]
	return found && recorded.size == stamp.size && recorded.modTime.Equal(stamp.modTime)
}

// quiescentStamp identifies a store file with no pending WAL content.
func quiescentStamp(path string) (storeFileStamp, bool) {
	if path == "" {
		return storeFileStamp{}, false
	}
	if wal, err := os.Stat(path + "-wal"); err == nil && wal.Size() > 0 { // #nosec G703 -- local SQLite store path
		return storeFileStamp{}, false
	}
	info, err := os.Stat(path) // #nosec G703 -- local SQLite store path
	if err != nil {
		return storeFileStamp{}, false
	}
	return storeFileStamp{size: info.Size(), modTime: info.ModTime()}, true
}

func copyStoreFile(srcPath, dstPath string) (err error) {
	in, err := os.Open(srcPath) // #nosec G703 -- local SQLite store path
	if err != nil {
		return fmt.Errorf("open store copy source: %w", err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil { // #nosec G703 -- local SQLite store dir
		return fmt.Errorf("create store copy directory: %w", err)
	}
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, defaultDBFileMode) // #nosec G703 -- local SQLite store path
	if err != nil {
		return fmt.Errorf("create store copy: %w", err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close store copy: %w", closeErr)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy store: %w", err)
	}
	return nil
}
