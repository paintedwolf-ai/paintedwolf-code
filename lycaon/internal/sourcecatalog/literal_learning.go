package sourcecatalog

import (
	"errors"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/repochange"
)

const literalReadQuantum = 64 * 1024
const literalLearningByteCap = literalBuildWorkers * (2*literalBloomMaxWords*8 + 12*literalReadQuantum + 16*1024)

var errLiteralReadBusy = errors.New("content observation capacity is occupied")

func literalLearningCost(entry Entry) int64 {
	return int64(2*literalBloomWords(entry.Size)*8 + 12*literalReadQuantum + 16*1024)
}

// beginFileRead shares nonblocking working capacity across every content producer.
func (c *literalIndexCache) beginFileRead(rootPath string, entry Entry, watch repochange.WatchObservation, version fspath.FileObservation) *literalFileRead {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	if cached, ok := c.files.lookup(rootPath, entry.Path); watch.Valid() && ok && cached.size == entry.Size && cached.mode == entry.Mode && cached.modifiedNS == entry.Modified.UnixNano() && cached.watch == watch && cached.version == version {
		return nil
	}
	for active := range c.fileReads {
		if active.rootPath == rootPath && active.path == entry.Path && !active.invalidated {
			return nil
		}
	}
	charge := literalLearningCost(entry)
	if len(c.fileReads) >= literalBuildWorkers || c.workingBytes+charge > literalLearningByteCap {
		return nil
	}
	read := &literalFileRead{rootPath: rootPath, path: entry.Path, watch: watch, charge: charge, version: version}
	c.fileReads[read] = struct{}{}
	c.workingBytes += charge
	return read
}

func (c *literalIndexCache) closeFileRead(read *literalFileRead) {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	if _, active := c.fileReads[read]; active {
		delete(c.fileReads, read)
		c.workingBytes -= read.charge
	}
}
