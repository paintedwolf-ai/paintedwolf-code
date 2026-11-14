package recovery

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const (
	spaceReserveName = "space-reserve"
	// spaceReserveBytes covers the directories and records of one retention many times over.
	spaceReserveBytes = 1 << 20
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// ensureSpaceReserve sets room aside while there is some. A worker that fills the volume leaves
// no block for even the record of its own retention, and without one its cancel can never finish.
func (c *Coordinator) ensureSpaceReserve() {
	if _, err := os.Lstat(filepath.Join(c.cfg.RootDir, spaceReserveName)); err == nil {
		return
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: c.cfg.RootDir, Rel: spaceReserveName},
		Source:   io.LimitReader(zeroReader{}, spaceReserveBytes),
		Mode:     0o600,
	})
	if err != nil {
		slog.Warn("worker recovery has no space reserve", "error", err)
	}
}

// ReleaseSpaceReserve reports whether it freed anything. The next engine start sets it aside again.
func (c *Coordinator) ReleaseSpaceReserve() bool {
	return os.Remove(filepath.Join(c.cfg.RootDir, spaceReserveName)) == nil
}
