package sourcecatalog

import (
	"bytes"
	"context"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
)

var literalFixtureVersions struct {
	sync.Mutex
	sequence uint64
	paths    map[string]uint64
}

func noteLiteralFixtureWrite(path string) {
	literalFixtureVersions.Lock()
	defer literalFixtureVersions.Unlock()
	if literalFixtureVersions.paths == nil {
		literalFixtureVersions.paths = map[string]uint64{}
	}
	literalFixtureVersions.sequence++
	literalFixtureVersions.paths[path] = literalFixtureVersions.sequence
}

// fixtureFileVersion supplies explicit change facts when the platform lacks them.
func fixtureFileVersion(root string, entry Entry) fspath.FileObservation {
	version := fileVersion(root, entry)
	if version.Known {
		return version
	}
	path := filepath.Join(root, filepath.FromSlash(entry.Path))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fspath.FileObservation{}
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	literalFixtureVersions.Lock()
	sequence := literalFixtureVersions.paths[path]
	literalFixtureVersions.Unlock()
	return fspath.FileObservation{Known: true, Links: 1, Size: info.Size(), Identity: fspath.AllocationIdentity{Device: 1, Inode: h.Sum64()}, Changed: int64(sequence) ^ info.ModTime().UnixNano() ^ int64(info.Mode())}
}

func captureObserved(read *ContentRead, ctx context.Context, content []byte) bool {
	defer read.Close()
	_, err := io.Copy(io.Discard, read.Wrap(ctx, bytes.NewReader(content)))
	return err == nil && read.Learned()
}
