package bgprocess

import (
	"fmt"
	"strings"
	"time"
)

// WritePTY writes bytes to the pty main of a live handle.
func (r *Terminal) WritePTY(sessionID, handle string, data []byte) error {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return err
	}
	r.jobs.mu.Lock()
	running := proc.running
	pty := proc.pty
	kind := proc.kind
	r.jobs.mu.Unlock()
	if kind != processKindPTY || pty == nil {
		return fmt.Errorf("%w: handle %s", ErrNotPTY, handle)
	}
	if !running {
		return ErrProcessNotRunning
	}
	_, err = pty.Write(data)
	return err
}

// ReadPTY returns incremental main output since the handle's last read cursor,
// waiting until idle quiescence, timeout, or process exit.
func (r *Terminal) ReadPTY(sessionID, handle string, opts PTYReadOpts) (PTYReadResult, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return PTYReadResult{}, err
	}
	r.jobs.mu.Lock()
	kind := proc.kind
	r.jobs.mu.Unlock()
	if kind != processKindPTY {
		return PTYReadResult{}, fmt.Errorf("%w: handle %s", ErrNotPTY, handle)
	}

	idle := opts.Idle
	if idle <= 0 {
		idle = DefaultPTYIdle
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultPTYReadTimeout
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = r.ringBufferBytes
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(ptyReadPoll)
	defer ticker.Stop()

	r.jobs.mu.Lock()
	startCursor := proc.readCursor
	r.jobs.mu.Unlock()
	var text strings.Builder
	var truncated bool
	var pageContinuation bool
	var evictedBytes int64
	var availableThrough int64
	var lastGrowth time.Time
	gotData := false

	drain := func() {
		r.jobs.mu.Lock()
		defer r.jobs.mu.Unlock()
		remain := maxBytes - text.Len()
		if remain <= 0 {
			truncated = true
			pageContinuation = true
			return
		}
		chunk, next, available, evicted := proc.buffer.ReadPage(proc.readCursor, remain)
		availableThrough = available
		if evicted > 0 {
			evictedBytes += evicted
		}
		if chunk == "" && evicted == 0 {
			if proc.readCursor < available {
				truncated = true
				pageContinuation = true
			}
			return
		}
		text.WriteString(chunk)
		proc.readCursor = next
		if next < available {
			truncated = true
			pageContinuation = true
		}
		if evicted > 0 {
			truncated = true
		}
		gotData = true
		lastGrowth = time.Now()
	}

	for {
		drain()
		r.jobs.mu.Lock()
		exited := proc.hasExit
		r.jobs.mu.Unlock()

		if pageContinuation {
			return r.ptyReadResult(proc, text.String(), truncated, pageContinuation, evictedBytes, startCursor, availableThrough), nil
		}
		if gotData && time.Since(lastGrowth) >= idle {
			return r.ptyReadResult(proc, text.String(), truncated, pageContinuation, evictedBytes, startCursor, availableThrough), nil
		}
		if exited {
			drain()
			return r.ptyReadResult(proc, text.String(), truncated, pageContinuation, evictedBytes, startCursor, availableThrough), nil
		}
		if time.Now().After(deadline) {
			return r.ptyReadResult(proc, text.String(), truncated, pageContinuation, evictedBytes, startCursor, availableThrough), nil
		}

		remain := time.Until(deadline)
		select {
		case <-proc.done:
			drain()
			return r.ptyReadResult(proc, text.String(), truncated, pageContinuation, evictedBytes, startCursor, availableThrough), nil
		case <-ticker.C:
		case <-time.After(remain):
			return r.ptyReadResult(proc, text.String(), truncated, pageContinuation, evictedBytes, startCursor, availableThrough), nil
		}
	}
}

func (r *Terminal) ptyReadResult(proc *Process, text string, truncated, pageContinuation bool, evictedBytes, startCursor, availableThrough int64) PTYReadResult {
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	if availableThrough == 0 {
		availableThrough = proc.buffer.NextCursor()
	}
	out := PTYReadResult{
		Text:                   text,
		BytesReturned:          len(text),
		Truncated:              truncated || evictedBytes > 0,
		PageContinuation:       pageContinuation,
		EvictedBytes:           evictedBytes,
		StartCursor:            startCursor,
		NextCursor:             proc.readCursor,
		AvailableThroughCursor: availableThrough,
		Running:                proc.running,
		Boundary:               proc.boundary,
		Report:                 proc.facts.Report,
		Network:                proc.facts.MediatedNetwork(),
		Refusals:               proc.facts.Refusals(),
	}
	if proc.hasExit {
		code := proc.exitCode
		out.ExitCode = &code
	}
	return out
}
