// Package libraryworker runs an in-process scanner implementation in a managed child process.
package libraryworker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/scanworker"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/workscope"
	"github.com/lycaon/lycaon/pkg/api"
)

// stderrTail bounds the diagnostics kept from a worker.
const stderrTail = 64 << 10

// Scanner reuses a worker process and its compiled rules across requests.
type Scanner struct {
	fingerprintKey []byte
	advisories     string
	id             string
	impl           string
	jobs           int
	categories     []api.ScanCategory
	priority       exec.ProcessPriority

	work   workscope.Group
	mu     sync.Mutex
	worker *resident
}

type Options struct {
	FingerprintKey []byte
	// AdvisoryDatabase is a provisioned OSV export the worker matches against.
	AdvisoryDatabase string
	ID               string
	Impl             string
	Jobs             int
	Categories       []api.ScanCategory
	ProcessPriority  exec.ProcessPriority
}

func New(opts Options) *Scanner {
	priority := opts.ProcessPriority
	if priority == "" {
		priority = exec.ProcessPriorityBelowNormal
	}
	return &Scanner{
		fingerprintKey: append([]byte(nil), opts.FingerprintKey...), advisories: opts.AdvisoryDatabase,
		id: strings.TrimSpace(opts.ID), impl: strings.TrimSpace(opts.Impl), jobs: opts.Jobs,
		categories: append([]api.ScanCategory(nil), opts.Categories...), priority: priority,
	}
}

func (s *Scanner) ID() string { return s.id }

func (s *Scanner) Categories() []api.ScanCategory {
	return append([]api.ScanCategory(nil), s.categories...)
}

// Run replaces an exited worker once; cancellation stops its process.
func (s *Scanner) Run(ctx context.Context, req scan.ScanRequest) (*scanoutput.Result, error) {
	ctx, finish, err := s.work.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	payload, err := surveyjson.Marshal(scanworker.Request{
		FingerprintKey: s.fingerprintKey, AdvisoryDatabase: s.advisories, Impl: s.impl, ID: s.id, Jobs: s.jobs, Scan: req,
	})
	if err != nil {
		return nil, fmt.Errorf("encode library scan worker request: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		worker, err := s.ensureWorker(ctx)
		if err != nil {
			return nil, err
		}
		result, err := worker.exchange(ctx, payload)
		if err == nil {
			return s.decode(result, worker)
		}
		s.worker = nil
		worker.stop()
		if ctx.Err() != nil || attempt == 1 || !worker.exited() {
			return nil, fmt.Errorf("library scan worker: %w%s", err, s.stderrSuffix(worker.stderrBytes()))
		}
		// The worker had exited before this request; start one and retry.
	}
	return nil, fmt.Errorf("library scan worker unavailable")
}

// Retire seals a replaced generation and lets admitted scans finish naturally.
// Shutdown cancellation interrupts the drain and stops its remaining requests.
func (s *Scanner) Retire(ctx context.Context) error {
	s.work.Seal()
	if err := s.work.Wait(ctx); err != nil {
		return s.Close(ctx)
	}
	return s.Close(ctx)
}

// Close seals scanner admission, cancels active requests, and joins its worker.
func (s *Scanner) Close(ctx context.Context) error {
	// Final shutdown must join owned processes even if the caller is canceled.
	ctx = context.WithoutCancel(ctx)
	s.work.Stop()
	if err := s.work.Wait(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	worker := s.worker
	s.worker = nil
	s.mu.Unlock()
	if worker != nil {
		worker.stop()
	}
	return nil
}

func (s *Scanner) ensureWorker(ctx context.Context) (*resident, error) {
	if s.worker != nil && !s.worker.exited() {
		return s.worker, nil
	}
	worker, err := startResident(ctx, s.priority)
	if err != nil {
		return nil, err
	}
	s.worker = worker
	return worker, nil
}

func (s *Scanner) decode(raw []byte, worker *resident) (*scanoutput.Result, error) {
	var response scanworker.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode library scan worker response: %w%s", err, s.stderrSuffix(worker.stderrBytes()))
	}
	if response.Error != "" {
		return nil, fmt.Errorf("library scan worker: %s", s.diagnostic(response.Error))
	}
	if response.Result == nil {
		return nil, fmt.Errorf("library scan worker returned no result")
	}
	response.Result.SecretIdentities = response.SecretIdentities
	return response.Result, nil
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// stderrSuffix screens diagnostic output before applying its limit.
func (s *Scanner) stderrSuffix(stderr []byte) string {
	return scan.DiagnosticSuffix("stderr", s.diagnosticSubject(), stderr)
}

// diagnostic screens worker errors that may contain scanned source.
func (s *Scanner) diagnostic(text string) string {
	return scan.DiagnosticExcerpt(s.diagnosticSubject(), []byte(text))
}

func (s *Scanner) diagnosticSubject() scan.DiagnosticSubject {
	return scan.DiagnosticSubject{Stream: scan.DiagnosticConsole, Categories: s.categories}
}
