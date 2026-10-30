package search

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const (
	grepWorkerMaximum = 8
	grepOutputChunk   = 64 << 10
	grepEventWindow   = 2
)

func grepWorkerCount() int { return min(max(runtime.GOMAXPROCS(0), 1), grepWorkerMaximum) }

type grepFileJob struct {
	entry         entry
	names, walked bool
	events        chan grepFileEvent
	result        grepFileResult
}

type grepFileResult struct {
	err      error
	selected bool
	status   int
	stats    contentSearchStats
	json     rgStats
}

// grepFiles limits file work and reduces its streams in traversal order.
type grepFiles struct {
	run        *grepRun
	template   grepRun
	invocation invocation
	maximum    int64
	mapped     bool
	failure    error
	abandoned  []*grepFileJob
	ctx        context.Context
	cancel     context.CancelFunc
	jobs       chan *grepFileJob
	pending    []*grepFileJob
	workers    sync.WaitGroup
	window     int
	release    func()
}

func newGrepFiles(r *grepRun, workers int) (*grepFiles, error) {
	workers = min(max(workers, 1), grepWorkerMaximum)
	ctx, cancel := context.WithCancel(r.inv.ctx)
	p := &grepFiles{run: r, template: *r, invocation: *r.inv, maximum: r.inv.out.maximum, mapped: r.inv.out.mappings != nil, ctx: ctx, cancel: cancel, jobs: make(chan *grepFileJob, workers), window: workers * 2}
	if r.json != nil {
		p.template.json = &rgJSON{started: r.json.started}
	}
	if memory := r.inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(ctx, int64((p.window*grepEventWindow+workers)*grepOutputChunk))
		if err != nil {
			cancel()
			return nil, err
		}
		p.release = release
	}
	for range workers {
		p.workers.Add(1)
		go p.worker()
	}
	return p, nil
}

func (p *grepFiles) submit(e entry, names, walked bool) error {
	job := &grepFileJob{entry: e, names: names, walked: walked, events: make(chan grepFileEvent, grepEventWindow)}
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.jobs <- job:
		p.pending = append(p.pending, job)
	}
	if len(p.pending) >= p.window {
		return p.next()
	}
	return nil
}

func (p *grepFiles) next() (finalErr error) {
	job := p.pending[0]
	finished := false
	defer func() {
		if finalErr != nil {
			p.failure = finalErr
			p.cancel()
			if !finished {
				p.abandoned = append(p.abandoned, job)
			}
		}
	}()
	p.pending = p.pending[1:]
	base := p.run.inv.out.written
	if err := p.run.inv.out.flush(); err != nil {
		return err
	}
	for event := range job.events {
		if err := p.apply(event, base); err != nil {
			return err
		}
	}
	finished = true
	p.run.selected = p.run.selected || job.result.selected
	p.run.inv.status = max(p.run.inv.status, job.result.status)
	p.addStats(job.result)
	return job.result.err
}

func (p *grepFiles) addStats(result grepFileResult) {
	stats := &p.run.index.stats
	stats.candidates += result.stats.candidates
	stats.eligible += result.stats.eligible
	stats.captured += result.stats.captured
	stats.pruned += result.stats.pruned
	stats.learned += result.stats.learned
	if p.run.json != nil {
		p.run.json.total.add(result.json)
	}
}

func (r *grepRun) flushFiles() error {
	if r.files == nil {
		return nil
	}
	if r.files.failure != nil {
		return r.files.failure
	}
	for len(r.files.pending) > 0 {
		if err := r.files.next(); err != nil {
			return err
		}
	}
	return nil
}

func (p *grepFiles) finish(err error) error {
	if drainErr := p.run.flushFiles(); drainErr != nil {
		err = drainErr
	}
	p.cancel()
	close(p.jobs)
	p.workers.Wait()
	for _, job := range append(p.pending, p.abandoned...) {
		p.addStats(job.result)
	}
	p.pending = nil
	if p.release != nil {
		p.release()
	}
	if errors.Is(err, errQuietMatch) {
		p.run.stopped = true
		return nil
	}
	return err
}

func (p *grepFiles) worker() {
	defer p.workers.Done()
	inv := p.invocation
	inv.ctx = p.ctx
	walker := newWalker(&inv)
	defer walker.close()
	for {
		select {
		case <-p.ctx.Done():
			return
		case job, ok := <-p.jobs:
			if !ok {
				return
			}
			p.search(job, walker)
		}
	}
}

func (p *grepFiles) search(job *grepFileJob, walker *walker) {
	defer close(job.events)
	inv := p.invocation
	inv.ctx, inv.pins, inv.flushedPins, inv.releases = p.ctx, nil, 0, nil
	inv.status = 0
	writer := &grepFileWriter{ctx: p.ctx, events: job.events}
	inv.stderr = writer.errorWriter()
	inv.out = newOutput(p.ctx, writer, p.maximum)
	if !p.mapped {
		inv.out.mappings = nil
	}
	inv.out.beforeWrite = func() error {
		if len(inv.pins) == inv.flushedPins {
			return nil
		}
		pins := append([]toolcontract.ExecutionRevision(nil), inv.pins[inv.flushedPins:]...)
		if err := writer.send(grepFileEvent{pins: pins}); err != nil {
			return err
		}
		inv.flushedPins = len(inv.pins)
		return nil
	}
	local := p.template
	local.inv, local.walker, local.files = &inv, walker, nil
	local.selected, local.stopped = false, false
	local.index.stats = contentSearchStats{}
	if local.json != nil {
		local.json = &rgJSON{started: local.json.started}
	}
	err := local.searchFileLive(job.entry, job.names, job.walked)
	if flushErr := inv.out.flush(); err == nil {
		err = flushErr
	}
	if pinErr := inv.out.beforeWrite(); err == nil {
		err = pinErr
	}
	for _, release := range inv.releases {
		release()
	}
	job.result = grepFileResult{err: err, selected: local.selected, status: inv.status, stats: local.index.stats}
	if local.json != nil {
		job.result.json = local.json.total
	}
}
