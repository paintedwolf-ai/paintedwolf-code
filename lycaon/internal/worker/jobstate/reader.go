package jobstate

import (
	"errors"

	"github.com/lycaon/lycaon/internal/db"
)

// ErrNoDataDir refuses stored paths on a handle that names no directory, such as a store in memory.
var ErrNoDataDir = errors.New("durable storage requires an on-disk main database")

// Reader maps the job rows of one installation. Stored branch and baseline
// addresses resolve against its data directory, in the spelling it was configured with.
type Reader struct{ dataDir string }

func NewReader(database db.Handle) Reader { return Reader{dataDir: db.DataDir(database)} }

func (r Reader) DataDir() (string, error) {
	if r.dataDir == "" {
		return "", ErrNoDataDir
	}
	return r.dataDir, nil
}
