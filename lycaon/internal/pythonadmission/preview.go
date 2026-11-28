package pythonadmission

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

var (
	// ErrMissingWorkspace is returned when StartPreviewListener is called without a WorkspaceAdapter.
	ErrMissingWorkspace = errors.New("preview listener requires a WorkspaceAdapter (fail closed)")
	// ErrMissingJobRegistry is returned when StartPreviewListener is called without a SessionJobRegistry.
	ErrMissingJobRegistry = errors.New("preview listener requires a SessionJobRegistry (fail closed)")
	// ErrUnsupportedPreviewForm is returned when `python[3] -m http.server`
	// uses flags or forms outside the cataloged static-preview subset.
	ErrUnsupportedPreviewForm = errors.New("unsupported http.server preview form")
	// ErrPreviewModuleShadowed is returned when a project module shadows the
	// bundled stdlib http.server module, so the native preview binding must
	// not hijack execution.
	ErrPreviewModuleShadowed = errors.New("workspace module shadows stdlib http.server")
	// ErrPreviewDenied is returned when listener admission refuses the preview bind.
	ErrPreviewDenied = errors.New("preview listener admission refused")
)

const defaultPreviewMaxResponseBytes int64 = 16 * 1024 * 1024

// SessionJobRegistry tracks live preview listener jobs owned by a session.
type SessionJobRegistry interface {
	RegisterPreviewJob(ctx context.Context, rec PreviewListenerRecord) error
	UnregisterPreviewJob(ctx context.Context, jobID string, rec PreviewListenerRecord) error
}

// MemorySessionJobRegistry is a thread-safe in-memory SessionJobRegistry.
type MemorySessionJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]PreviewListenerRecord
}

// NewMemorySessionJobRegistry constructs an empty MemorySessionJobRegistry.
func NewMemorySessionJobRegistry() *MemorySessionJobRegistry {
	return &MemorySessionJobRegistry{
		jobs: make(map[string]PreviewListenerRecord),
	}
}

// RegisterPreviewJob registers an active preview listener job.
func (r *MemorySessionJobRegistry) RegisterPreviewJob(_ context.Context, rec PreviewListenerRecord) error {
	if rec.JobID == "" {
		return errors.New("preview job missing job_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.jobs[rec.JobID]; exists {
		return fmt.Errorf("preview job %q already registered", rec.JobID)
	}
	r.jobs[rec.JobID] = rec
	return nil
}

// UnregisterPreviewJob removes a stopped preview listener job.
func (r *MemorySessionJobRegistry) UnregisterPreviewJob(_ context.Context, jobID string, _ PreviewListenerRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, jobID)
	return nil
}

// Get returns a registered active preview job by jobID.
func (r *MemorySessionJobRegistry) Get(jobID string) (PreviewListenerRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.jobs[jobID]
	return rec, ok
}

// ActiveJobs returns a snapshot of all currently registered active preview jobs.
func (r *MemorySessionJobRegistry) ActiveJobs() []PreviewListenerRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PreviewListenerRecord, 0, len(r.jobs))
	for _, rec := range r.jobs {
		out = append(out, rec)
	}
	return out
}

// PreviewCommandSpec holds the parsed arguments of a cataloged
// `python[3] -m http.server` invocation.
type PreviewCommandSpec struct {
	BindAddress string
	Port        int
	Directory   string
	Help        bool
}

// ParseHTTPServerArgs parses the documented static-preview forms of
// `python[3] -m http.server`:
//   - `-b` / `--bind <addr>` (default `127.0.0.1`)
//   - `-d` / `--directory <dir>` (default `.`)
//   - `-h` / `--help`
//   - optional positional `[port]` (0..65535, default `8000`)
//
// Unsupported flags (such as `--cgi`, `--protocol`) return ErrUnsupportedPreviewForm.
func ParseHTTPServerArgs(args []string) (PreviewCommandSpec, error) {
	spec := PreviewCommandSpec{
		BindAddress: "127.0.0.1",
		Port:        8000,
		Directory:   ".",
	}
	portSet := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			spec.Help = true
		case arg == "-b" || arg == "--bind":
			if i+1 >= len(args) {
				return PreviewCommandSpec{}, fmt.Errorf("%w: %s requires an address", ErrUnsupportedPreviewForm, arg)
			}
			i++
			spec.BindAddress = args[i]
		case strings.HasPrefix(arg, "--bind="):
			spec.BindAddress = strings.TrimPrefix(arg, "--bind=")
		case arg == "-d" || arg == "--directory":
			if i+1 >= len(args) {
				return PreviewCommandSpec{}, fmt.Errorf("%w: %s requires a directory", ErrUnsupportedPreviewForm, arg)
			}
			i++
			spec.Directory = args[i]
		case strings.HasPrefix(arg, "--directory="):
			spec.Directory = strings.TrimPrefix(arg, "--directory=")
		case strings.HasPrefix(arg, "-"):
			return PreviewCommandSpec{}, fmt.Errorf("%w: flag %q is not supported by static preview", ErrUnsupportedPreviewForm, arg)
		default:
			if portSet {
				return PreviewCommandSpec{}, fmt.Errorf("%w: unexpected extra positional argument %q", ErrUnsupportedPreviewForm, arg)
			}
			p, err := strconv.Atoi(arg)
			if err != nil || p < 0 || p > 65535 {
				return PreviewCommandSpec{}, fmt.Errorf("%w: invalid port %q", ErrUnsupportedPreviewForm, arg)
			}
			spec.Port = p
			portSet = true
		}
	}

	if strings.TrimSpace(spec.BindAddress) == "" {
		spec.BindAddress = "127.0.0.1"
	}
	if strings.TrimSpace(spec.Directory) == "" {
		spec.Directory = "."
	}
	return spec, nil
}

// CheckStdlibHTTPServerSelected reports whether the bundled stdlib
// `http.server` module is selected (true) or shadowed by a project module
// under cwd (`http/server.py` or `http.py`, returning false and the shadowing
// path).
func CheckStdlibHTTPServerSelected(ctx context.Context, workspace pythonruntime.WorkspaceAdapter, cwd string) (bool, string) {
	if workspace == nil {
		return true, ""
	}
	candidates := []string{
		joinWorkspacePath(cwd, "http/server.py"),
		joinWorkspacePath(cwd, "http.py"),
	}
	for _, cand := range candidates {
		if _, errno := workspace.Stat(ctx, cand); errno == pythonruntime.ErrnoSuccess {
			return false, cand
		}
	}
	return true, ""
}

// PreviewConfig configures StartPreviewListener.
type PreviewConfig struct {
	ExecutionID      string
	OperationID      string
	CallID           string
	JobID            string
	CWD              string
	Args             []string
	Workspace        pythonruntime.WorkspaceAdapter
	Observer         pythonruntime.OperationObserver
	JobRegistry      SessionJobRegistry
	StopLatch        *pythonruntime.StopLatch
	MaxResponseBytes int64
	Clock            pythonruntime.Clock
	Evidence         EvidenceSink
	DeclaredFrame    *pythonruntime.DeclaredFrame
}

// PreviewListener is a host-managed static file HTTP listener serving scoped
// workspace reads only.
type PreviewListener struct {
	jobID       string
	execID      string
	callID      string
	directory   string
	listener    net.Listener
	server      *http.Server
	jobRegistry SessionJobRegistry
	clock       pythonruntime.Clock
	evidence    EvidenceSink
	startedAt   time.Time

	stopOnce  sync.Once
	mu        sync.Mutex
	active    bool
	stoppedAt time.Time
}

var previewJobSeq atomic.Uint64

// StartPreviewListener fails closed if Workspace, Observer, or JobRegistry is
// nil, validates arguments, honors workspace module shadowing, authorizes the
// listener through OperationObserver.Admit(EffectPreviewListener), binds a
// host-managed net.Listener, registers the live job with JobRegistry, and
// serves bounded, admitted, StopLatch-aware workspace reads.
func StartPreviewListener(ctx context.Context, cfg PreviewConfig) (*PreviewListener, error) {
	if cfg.Workspace == nil {
		return nil, ErrMissingWorkspace
	}
	if cfg.Observer == nil {
		return nil, ErrMissingObserver
	}
	if cfg.JobRegistry == nil {
		return nil, ErrMissingJobRegistry
	}

	spec, err := ParseHTTPServerArgs(cfg.Args)
	if err != nil {
		return nil, err
	}
	if spec.Help {
		return nil, fmt.Errorf("%w: --help should be handled by help renderer", ErrUnsupportedPreviewForm)
	}

	if selected, shadowed := CheckStdlibHTTPServerSelected(ctx, cfg.Workspace, cfg.CWD); !selected {
		return nil, fmt.Errorf("%w: %s", ErrPreviewModuleShadowed, shadowed)
	}

	dirPath := joinWorkspacePath(cfg.CWD, spec.Directory)
	if hasPathEscape(dirPath) {
		return nil, fmt.Errorf("%w: directory %q escapes workspace", ErrPreviewDenied, spec.Directory)
	}

	clk := cfg.Clock
	if clk == nil {
		clk = pythonruntime.NewSystemClock()
	}
	ev := cfg.Evidence
	if ev == nil {
		ev = NewMemoryEvidenceStore()
	}
	jobID := cfg.JobID
	if jobID == "" {
		jobID = fmt.Sprintf("preview-%d", previewJobSeq.Add(1))
	}
	opID := cfg.OperationID
	if opID == "" {
		opID = "preview-bind-" + jobID
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultPreviewMaxResponseBytes
	}

	dec, admitErr := cfg.Observer.Admit(ctx, pythonruntime.ReachedOperation{
		OperationID: opID,
		ExecutionID: cfg.ExecutionID,
		Family:      pythonruntime.EffectPreviewListener,
		Operation:   "preview.bind",
		Target: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RelPath:   dirPath,
		},
		Args: map[string]any{
			"bind":      spec.BindAddress,
			"port":      spec.Port,
			"directory": dirPath,
			"job_id":    jobID,
		},
		DeclaredFrame: cfg.DeclaredFrame,
	})
	if admitErr != nil {
		return nil, admitErr
	}
	if dec.Kind != pythonruntime.DecisionAllow {
		return nil, fmt.Errorf("%w: %s", ErrPreviewDenied, dec.Reason)
	}

	addr := net.JoinHostPort(spec.BindAddress, strconv.Itoa(spec.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		cfg.Observer.RecordOutcome(ctx, opID, pythonruntime.OperationOutcome{
			Succeeded: false,
			Error:     err.Error(),
		})
		return nil, fmt.Errorf("bind preview listener on %s: %w", addr, err)
	}

	handler := newScopedWorkspaceHandler(cfg.Workspace, cfg.Observer, cfg.StopLatch, cfg.ExecutionID, dirPath, maxBytes)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	now := clk.Now().UTC()
	pl := &PreviewListener{
		jobID:       jobID,
		execID:      cfg.ExecutionID,
		callID:      cfg.CallID,
		directory:   dirPath,
		listener:    ln,
		server:      srv,
		jobRegistry: cfg.JobRegistry,
		clock:       clk,
		evidence:    ev,
		startedAt:   now,
		active:      true,
	}

	if err := cfg.JobRegistry.RegisterPreviewJob(ctx, pl.Record()); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("register preview job: %w", err)
	}

	ev.RecordPreviewListener(pl.Record())
	cfg.Observer.RecordOutcome(ctx, opID, pythonruntime.OperationOutcome{
		Succeeded: true,
	})

	if cfg.StopLatch != nil {
		cfg.StopLatch.RegisterSealer(func() {
			_ = pl.Stop(context.Background())
		})
	}

	go func() {
		_ = srv.Serve(ln)
	}()

	return pl, nil
}

// JobID returns the session job identifier for this listener.
func (p *PreviewListener) JobID() string { return p.jobID }

// Address returns the bound listener host:port string.
func (p *PreviewListener) Address() string { return p.listener.Addr().String() }

// Port returns the actual bound TCP port.
func (p *PreviewListener) Port() int {
	if tcpAddr, ok := p.listener.Addr().(*net.TCPAddr); ok {
		return tcpAddr.Port
	}
	return 0
}

// Active reports whether the preview listener is currently running.
func (p *PreviewListener) Active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

// Record returns a PreviewListenerRecord snapshot for PY05 evidence.
func (p *PreviewListener) Record() PreviewListenerRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PreviewListenerRecord{
		JobID:       p.jobID,
		ExecutionID: p.execID,
		CallID:      p.callID,
		Address:     p.Address(),
		Port:        p.Port(),
		Directory:   p.directory,
		Active:      p.active,
		StartedAt:   p.startedAt,
		StoppedAt:   p.stoppedAt,
	}
}

// Stop cleanly shuts down the HTTP server, releases the TCP listener,
// unregisters the session job, and records the inactive PreviewListenerRecord.
func (p *PreviewListener) Stop(ctx context.Context) error {
	var stopErr error
	p.stopOnce.Do(func() {
		stopErr = p.server.Shutdown(ctx)
		_ = p.listener.Close()
		p.mu.Lock()
		p.active = false
		p.stoppedAt = p.clock.Now().UTC()
		p.mu.Unlock()
		rec := p.Record()
		if p.jobRegistry != nil {
			_ = p.jobRegistry.UnregisterPreviewJob(ctx, p.jobID, rec)
		}
		p.evidence.RecordPreviewListener(rec)
	})
	return stopErr
}

func newScopedWorkspaceHandler(
	workspace pythonruntime.WorkspaceAdapter,
	observer pythonruntime.OperationObserver,
	stopLatch *pythonruntime.StopLatch,
	execID string,
	baseDir string,
	maxResponseBytes int64,
) http.Handler {
	var reqSeq atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stopLatch != nil && stopLatch.Stopped() {
			http.Error(w, "preview stopped", http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.Contains(r.URL.Path, "..") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		cleanReq := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		targetPath := joinWorkspacePath(baseDir, cleanReq)
		if hasPathEscape(targetPath) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Re-check read authority through OperationObserver on every served request.
		readOpID := fmt.Sprintf("preview-read-%d", reqSeq.Add(1))
		dec, admitErr := observer.Admit(r.Context(), pythonruntime.ReachedOperation{
			OperationID: readOpID,
			ExecutionID: execID,
			Family:      pythonruntime.EffectFilesystemRead,
			Operation:   "preview.read",
			Target: pythonruntime.CanonicalTarget{
				Namespace: pythonruntime.NamespaceWorkspace,
				RelPath:   targetPath,
			},
			Args: map[string]any{
				"method": r.Method,
				"path":   r.URL.Path,
			},
		})
		if admitErr != nil || dec.Kind != pythonruntime.DecisionAllow {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		info, errno := workspace.Stat(r.Context(), targetPath)
		if errno == pythonruntime.ErrnoSuccess && info.IsDir {
			targetPath = joinWorkspacePath(targetPath, "index.html")
			// Re-admit resolved index.html target as well.
			idxOpID := fmt.Sprintf("preview-read-%d", reqSeq.Add(1))
			idxDec, idxErr := observer.Admit(r.Context(), pythonruntime.ReachedOperation{
				OperationID: idxOpID,
				ExecutionID: execID,
				Family:      pythonruntime.EffectFilesystemRead,
				Operation:   "preview.read",
				Target: pythonruntime.CanonicalTarget{
					Namespace: pythonruntime.NamespaceWorkspace,
					RelPath:   targetPath,
				},
			})
			if idxErr != nil || idxDec.Kind != pythonruntime.DecisionAllow {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			readOpID = idxOpID
			info, errno = workspace.Stat(r.Context(), targetPath)
		}
		if errno != pythonruntime.ErrnoSuccess {
			observer.RecordOutcome(r.Context(), readOpID, pythonruntime.OperationOutcome{
				Succeeded: false,
				Errno:     errno,
			})
			http.NotFound(w, r)
			return
		}
		if maxResponseBytes > 0 && info.Size > maxResponseBytes {
			observer.RecordOutcome(r.Context(), readOpID, pythonruntime.OperationOutcome{
				Succeeded: false,
				Error:     "response exceeds max_response_bytes",
			})
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		f, errno := workspace.Open(r.Context(), pythonruntime.OpenRequest{
			Path:  targetPath,
			Flags: pythonruntime.OpenRead,
		})
		if errno != pythonruntime.ErrnoSuccess {
			observer.RecordOutcome(r.Context(), readOpID, pythonruntime.OperationOutcome{
				Succeeded: false,
				Errno:     errno,
			})
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		if ct := mime.TypeByExtension(path.Ext(targetPath)); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		if info.Size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			observer.RecordOutcome(r.Context(), readOpID, pythonruntime.OperationOutcome{Succeeded: true})
			return
		}

		written, streamErr := streamBoundedGuestFile(w, f, stopLatch, maxResponseBytes)
		outcome := pythonruntime.OperationOutcome{
			Succeeded:        streamErr == nil,
			BytesTransferred: written,
		}
		if streamErr != nil {
			outcome.Error = streamErr.Error()
		}
		observer.RecordOutcome(r.Context(), readOpID, outcome)
	})
}

func streamBoundedGuestFile(
	w http.ResponseWriter,
	f pythonruntime.GuestFile,
	stopLatch *pythonruntime.StopLatch,
	maxBytes int64,
) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		if stopLatch != nil && stopLatch.Stopped() {
			return total, pythonruntime.ErrStreamSealed
		}
		readCap := len(buf)
		if maxBytes > 0 {
			rem := maxBytes - total
			if rem <= 0 {
				return total, errors.New("preview response exceeded max_response_bytes")
			}
			if int64(readCap) > rem {
				readCap = int(rem)
			}
		}
		n, errno := f.Read(buf[:readCap])
		if n > 0 {
			if stopLatch != nil && stopLatch.Stopped() {
				return total, pythonruntime.ErrStreamSealed
			}
			wn, wErr := w.Write(buf[:n])
			total += int64(wn)
			if wErr != nil {
				return total, wErr
			}
		}
		if errno != pythonruntime.ErrnoSuccess {
			return total, errno
		}
		if n == 0 {
			return total, nil
		}
	}
}

func joinWorkspacePath(base, rel string) string {
	base = strings.TrimSpace(base)
	rel = strings.TrimSpace(rel)
	if base == "" || base == "." {
		if rel == "" || rel == "." {
			return "."
		}
		return path.Clean(rel)
	}
	if rel == "" || rel == "." {
		return path.Clean(base)
	}
	return path.Clean(base + "/" + rel)
}
