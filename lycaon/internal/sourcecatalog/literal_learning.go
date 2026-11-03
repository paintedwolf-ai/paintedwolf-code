package sourcecatalog

import (
	"github.com/lycaon/lycaon/internal/fspath"
)

const literalReadQuantum = 64 * 1024
const literalLearningByteCap = literalBuildWorkers * (2*literalBloomMaxWords*8 + 12*literalReadQuantum + 16*1024)

func literalLearningCost(entry Entry) int64 {
	return int64(2*literalBloomWords(entry.Size)*8 + 12*literalReadQuantum + 16*1024)
}

// beginFileRead shares nonblocking working capacity across every content producer.
func (c *literalIndexCache) beginFileRead(rootPath string, entry Entry, version fspath.FileObservation) *literalFileRead {
	if !version.Known || version.Size <= 0 || version.Links == 0 {
		return nil
	}
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	if c.drained {
		return nil
	}
	if cached, ok := c.files.lookup(rootPath, entry.Path); ok && cached.version == version && !cached.unavailable {
		return nil
	}
	for active := range c.fileReads {
		if active.rootPath == rootPath && active.path == entry.Path && !active.invalidated {
			return nil
		}
	}
	observed := entry
	observed.Size = version.Size
	charge := literalLearningCost(observed)
	if len(c.fileReads) >= literalBuildWorkers || c.workingBytes+charge > literalLearningByteCap {
		return nil
	}
	read := &literalFileRead{rootPath: rootPath, path: entry.Path, charge: charge, version: version}
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
		close(c.available)
		c.available = make(chan struct{})
	}
}
