package jobstate

import "errors"

// ErrNoDataDir refuses stored paths when no directory is known, as for a store in memory.
var ErrNoDataDir = errors.New("durable storage requires an on-disk main database")

// Reader maps the job rows of one installation. Stored branch and baseline
// addresses resolve against its data directory, in the spelling it was configured with.
type Reader struct{ dataDir string }

func NewReader(dataDir string) Reader { return Reader{dataDir: dataDir} }

func (r Reader) DataDir() (string, error) {
	if r.dataDir == "" {
		return "", ErrNoDataDir
	}
	return r.dataDir, nil
}
