package projectsource

import (
	"context"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/sourceledger"
)

const sourceFingerprintReaders = 4

type sourceFingerprintJob struct {
	entry   sourceledger.RecoveryEntry
	info    fs.FileInfo
	sha     string
	err     error
	release func()
}

// The existing read arena is partitioned between a bounded page of readers.
// Canonical framing remains serial and follows the admitted lexical walk.
type sourceFingerprintPage struct {
	ctx        context.Context
	cancel     context.CancelFunc
	open       func(string) (*os.File, error)
	readlink   func(string) (string, error)
	output     io.Writer
	progress   *sourceWorkProgress
	allocation *sourceTreeAllocation
	arena      []byte
	jobs       [sourceFingerprintReaders]sourceFingerprintJob
	count      int
	work       sync.WaitGroup
	release    func()
}

func newSourceFingerprintPage(ctx context.Context, output io.Writer, open func(string) (*os.File, error), readlink func(string) (string, error), progress *sourceWorkProgress, allocation *sourceTreeAllocation, scratch []byte) (*sourceFingerprintPage, error) {
	if len(scratch) < sourceFingerprintReaders {
		return nil, ErrSourceRecoveryFailed
	}
	// Fixed job, goroutine and callback metadata precede construction/launch;
	// retained path bytes have separate leases before cloning.
	release, err := allocation.memory.ReserveProgramState(ctx, sourceFingerprintReaders*(8<<10))
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	p := &sourceFingerprintPage{ctx: child, cancel: cancel, open: open, readlink: readlink, output: output, progress: progress, allocation: allocation, arena: scratch, release: release}
	return p, nil
}

func (p *sourceFingerprintPage) Add(rel string, info fs.FileInfo) error {
	if err := p.allocation.observe(p.ctx, info); err != nil {
		return err
	}
	if !info.IsDir() {
		p.progress.files++
	}
	width := len(p.arena) / sourceFingerprintReaders
	// A larger file keeps the original complete read buffer, after prior
	// readers join. Files fitting a partition can overlap without extra reads.
	serial := !info.Mode().IsRegular() || info.Size() > int64(width)
	if serial {
		if err := p.Flush(); err != nil {
			return err
		}
	}
	release, err := p.allocation.memory.ReserveProgramState(p.ctx, 4*int64(len(rel)))
	if err != nil {
		return err
	}
	buffer := p.arena[p.count*width : (p.count+1)*width : (p.count+1)*width]
	if serial {
		buffer = p.arena
	}
	job := &p.jobs[p.count]
	job.entry, job.info, job.release = sourceledger.RecoveryEntry{Path: strings.Clone(rel), Mode: uint32(info.Mode())}, info, release
	p.count++
	if info.Mode().IsRegular() {
		p.work.Add(1)
		go func(buffer []byte) {
			defer p.work.Done()
			job.sha, job.err = fingerprintSourceFile(p.ctx, job.info, func() (*os.File, error) { return p.open(job.entry.Path) }, p.progress, buffer)
		}(buffer)
	} else if info.Mode()&os.ModeSymlink != 0 {
		job.entry.Link, job.err = p.readlink(job.entry.Path)
	}
	if p.count == len(p.jobs) || serial {
		return p.Flush()
	}
	return nil
}

func (p *sourceFingerprintPage) Flush() error {
	p.work.Wait()
	var result error
	for i := 0; i < p.count; i++ {
		job := &p.jobs[i]
		if result == nil {
			result = p.ctx.Err()
		}
		if result == nil {
			result = job.err
		}
		if result == nil {
			job.entry.SHA = job.sha
			result = writeSourceTreeEntry(p.output, job.entry)
			if result == nil {
				p.progress.entry()
			}
		}
		job.reset()
	}
	p.count = 0
	return result
}

func (job *sourceFingerprintJob) reset() {
	if job.release != nil {
		job.release()
	}
	*job = sourceFingerprintJob{}
}

func (p *sourceFingerprintPage) Close() {
	if p.release == nil {
		return
	}
	p.cancel()
	p.work.Wait()
	for i := range p.jobs {
		p.jobs[i].reset()
	}
	p.count = 0
	p.release()
	p.release = nil
}
