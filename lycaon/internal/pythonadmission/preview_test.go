package pythonadmission

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

type memGuestFile struct {
	data []byte
	pos  int
	info pythonruntime.StatInfo
}

func (f *memGuestFile) Read(p []byte) (int, pythonruntime.Errno) {
	if f.pos >= len(f.data) {
		return 0, pythonruntime.ErrnoSuccess
	}
	n := copy(p, f.data[f.pos:])
	f.pos += n
	return n, pythonruntime.ErrnoSuccess
}

func (f *memGuestFile) Write(_ []byte) (int, pythonruntime.Errno)    { return 0, pythonruntime.ErrnoEROFS }
func (f *memGuestFile) Seek(off int64, _ int) (int64, pythonruntime.Errno) {
	f.pos = int(off)
	return int64(f.pos), pythonruntime.ErrnoSuccess
}
func (f *memGuestFile) Truncate(_ int64) pythonruntime.Errno         { return pythonruntime.ErrnoEROFS }
func (f *memGuestFile) Sync() pythonruntime.Errno                    { return pythonruntime.ErrnoSuccess }
func (f *memGuestFile) Stat() (pythonruntime.StatInfo, pythonruntime.Errno) { return f.info, pythonruntime.ErrnoSuccess }
func (f *memGuestFile) Close() pythonruntime.Errno                   { return pythonruntime.ErrnoSuccess }

type memWorkspace struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newMemWorkspace(files map[string]string) *memWorkspace {
	mw := &memWorkspace{
		files: make(map[string][]byte, len(files)),
		dirs:  map[string]bool{".": true},
	}
	for k, v := range files {
		clean := path.Clean(k)
		mw.files[clean] = []byte(v)
		for dir := path.Dir(clean); dir != "." && dir != "/"; dir = path.Dir(dir) {
			mw.dirs[dir] = true
		}
	}
	return mw
}

func (m *memWorkspace) Open(_ context.Context, req pythonruntime.OpenRequest) (pythonruntime.GuestFile, pythonruntime.Errno) {
	clean := path.Clean(req.Path)
	data, ok := m.files[clean]
	if !ok {
		return nil, pythonruntime.ErrnoENOENT
	}
	return &memGuestFile{
		data: bytes.Clone(data),
		info: pythonruntime.StatInfo{
			Target: pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: clean},
			Name:   path.Base(clean),
			Size:   int64(len(data)),
			Mode:   0o644,
		},
	}, pythonruntime.ErrnoSuccess
}

func (m *memWorkspace) Stat(_ context.Context, p string) (pythonruntime.StatInfo, pythonruntime.Errno) {
	clean := path.Clean(p)
	if data, ok := m.files[clean]; ok {
		return pythonruntime.StatInfo{
			Target: pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: clean},
			Name:   path.Base(clean),
			Size:   int64(len(data)),
			Mode:   0o644,
		}, pythonruntime.ErrnoSuccess
	}
	if m.dirs[clean] {
		return pythonruntime.StatInfo{
			Target: pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: clean},
			Name:   path.Base(clean),
			IsDir:  true,
			Mode:   fs.ModeDir | 0o755,
		}, pythonruntime.ErrnoSuccess
	}
	return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
}

func (m *memWorkspace) Lstat(ctx context.Context, p string) (pythonruntime.StatInfo, pythonruntime.Errno) {
	return m.Stat(ctx, p)
}
func (m *memWorkspace) ReadDir(_ context.Context, _ string) ([]pythonruntime.DirEntry, pythonruntime.Errno) {
	return nil, pythonruntime.ErrnoSuccess
}
func (m *memWorkspace) Mkdir(_ context.Context, _ string, _ fs.FileMode) pythonruntime.Errno {
	return pythonruntime.ErrnoEROFS
}
func (m *memWorkspace) Unlink(_ context.Context, _ string) pythonruntime.Errno { return pythonruntime.ErrnoEROFS }
func (m *memWorkspace) Rmdir(_ context.Context, _ string) pythonruntime.Errno  { return pythonruntime.ErrnoEROFS }
func (m *memWorkspace) Rename(_ context.Context, _, _ string) pythonruntime.Errno {
	return pythonruntime.ErrnoEROFS
}
func (m *memWorkspace) Readlink(_ context.Context, _ string) (string, pythonruntime.Errno) {
	return "", pythonruntime.ErrnoEINVAL
}
func (m *memWorkspace) Settle(_ context.Context, clean bool) (*pythonruntime.SettlementReport, error) {
	return &pythonruntime.SettlementReport{CleanCompletion: clean}, nil
}

func TestParseHTTPServerArgs(t *testing.T) {
	spec, err := ParseHTTPServerArgs([]string{"-b", "127.0.0.1", "-d", "site", "8080"})
	testutil.FailErr(t, "parse valid args", err)
	if spec.BindAddress != "127.0.0.1" || spec.Directory != "site" || spec.Port != 8080 {
		t.Fatalf("unexpected spec: %+v", spec)
	}

	specEq, err := ParseHTTPServerArgs([]string{"--bind=127.0.0.1", "--directory=dist", "0"})
	testutil.FailErr(t, "parse equals args", err)
	if specEq.Directory != "dist" || specEq.Port != 0 {
		t.Fatalf("unexpected equals spec: %+v", specEq)
	}

	for _, bad := range [][]string{
		{"--cgi"},
		{"--protocol", "HTTP/1.1"},
		{"--unknown"},
		{"8080", "9090"},
		{"notaport"},
	} {
		if _, err := ParseHTTPServerArgs(bad); !errors.Is(err, ErrUnsupportedPreviewForm) {
			t.Fatalf("args %v err = %v, want ErrUnsupportedPreviewForm", bad, err)
		}
	}
}

func TestPreviewListenerBindServeStopAndModuleShadowing(t *testing.T) {
	ws := newMemWorkspace(map[string]string{
		"site/index.html": "<h1>hello preview</h1>",
		"site/app.js":     "console.log('ok');",
	})
	store := NewMemoryEvidenceStore()
	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-preview-1",
		CallID:      "call-preview-1",
		Evidence:    store,
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})

	jobs := NewMemorySessionJobRegistry()
	pl, err := StartPreviewListener(context.Background(), PreviewConfig{
		ExecutionID: "exec-preview-1",
		CallID:      "call-preview-1",
		JobID:       "job-preview-1",
		Args:        []string{"--bind", "127.0.0.1", "--directory", "site", "0"},
		Workspace:   ws,
		Observer:    obs,
		JobRegistry: jobs,
		Evidence:    store,
	})
	testutil.FailErr(t, "start preview listener", err)
	defer func() { _ = pl.Stop(context.Background()) }()

	if !pl.Active() || pl.Port() <= 0 || pl.JobID() != "job-preview-1" {
		t.Fatalf("unexpected preview listener identity: active=%v port=%d job=%q", pl.Active(), pl.Port(), pl.JobID())
	}
	if _, ok := jobs.Get("job-preview-1"); !ok {
		t.Fatal("preview job was not registered in SessionJobRegistry")
	}

	// Serve index.html via scoped workspace reads.
	resp, err := http.Get("http://" + pl.Address() + "/")
	testutil.FailErr(t, "GET preview index", err)
	body, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "read preview body", err)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "<h1>hello preview</h1>" {
		t.Fatalf("GET / = (%d, %q)", resp.StatusCode, string(body))
	}

	// POST is refused (read-only static preview).
	postResp, err := http.Post("http://"+pl.Address()+"/app.js", "text/plain", strings.NewReader("x"))
	testutil.FailErr(t, "POST preview", err)
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", postResp.StatusCode)
	}

	// Clean stop releases listener and records inactive PreviewListenerRecord.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	testutil.FailErr(t, "stop preview listener", pl.Stop(stopCtx))
	if pl.Active() {
		t.Fatal("preview listener still active after Stop")
	}
	if _, ok := jobs.Get("job-preview-1"); ok {
		t.Fatal("preview job was not unregistered from SessionJobRegistry on Stop")
	}

	recs := store.PreviewListeners()
	if len(recs) != 2 || !recs[0].Active || recs[1].Active {
		t.Fatalf("expected [active=true, active=false] preview records, got %+v", recs)
	}

	// Workspace module shadowing: project http/server.py must prevent native preview bind!
	shadowedWS := newMemWorkspace(map[string]string{
		"http/server.py": "print('custom project server')",
	})
	_, shadowErr := StartPreviewListener(context.Background(), PreviewConfig{
		ExecutionID: "exec-preview-shadow",
		Args:        []string{"0"},
		Workspace:   shadowedWS,
		Observer:    obs,
		JobRegistry: jobs,
	})
	if !errors.Is(shadowErr, ErrPreviewModuleShadowed) {
		t.Fatalf("shadowed http.server err = %v, want ErrPreviewModuleShadowed", shadowErr)
	}
}

func TestPreviewListenerStoppedByStopLatchAndAdmissionRefusal(t *testing.T) {
	ws := newMemWorkspace(map[string]string{"index.html": "ok"})
	jobs := NewMemorySessionJobRegistry()

	// 1. Denied by observer -> does not bind.
	denyObs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-preview-deny",
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) PolicyDecision {
			if op.Family == pythonruntime.EffectPreviewListener {
				return PolicyDecision{Kind: pythonruntime.DecisionStop, Condition: ConditionEffectDenied, Reason: "listeners disabled"}
			}
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	_, err := StartPreviewListener(context.Background(), PreviewConfig{
		ExecutionID: "exec-preview-deny",
		Args:        []string{"0"},
		Workspace:   ws,
		Observer:    denyObs,
		JobRegistry: jobs,
	})
	if !errors.Is(err, ErrPreviewDenied) {
		t.Fatalf("denied preview err = %v, want ErrPreviewDenied", err)
	}

	// 2. Triggering StopLatch releases an active listener automatically.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	latch := pythonruntime.NewStopLatch(cancel)
	allowObs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-preview-latch",
		StopLatch:   latch,
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	pl, err := StartPreviewListener(context.Background(), PreviewConfig{
		ExecutionID: "exec-preview-latch",
		Args:        []string{"0"},
		Workspace:   ws,
		Observer:    allowObs,
		JobRegistry: jobs,
		StopLatch:   latch,
	})
	testutil.FailErr(t, "start latched preview", err)
	latch.Trigger(pythonruntime.TerminalStop{Code: pythonruntime.StopCancelled, Reason: "cancelled"})
	if pl.Active() {
		t.Fatal("preview listener remained active after StopLatch triggered")
	}
}

func TestPreviewListenerFailsClosedAndEnforcesPerRequestAdmissionAndBounds(t *testing.T) {
	ws := newMemWorkspace(map[string]string{
		"ok.txt":     "small",
		"secret.txt": "classified",
		"large.txt":  strings.Repeat("A", 2048),
	})
	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-preview-fc",
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) PolicyDecision {
			if op.Family == pythonruntime.EffectFilesystemRead && op.Target.RelPath == "secret.txt" {
				return PolicyDecision{Kind: pythonruntime.DecisionStop, Condition: ConditionProtectedSink, Reason: "secret blocked"}
			}
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	jobs := NewMemorySessionJobRegistry()

	// Fail closed on missing Workspace, Observer, or JobRegistry.
	if _, err := StartPreviewListener(context.Background(), PreviewConfig{Observer: obs, JobRegistry: jobs}); !errors.Is(err, ErrMissingWorkspace) {
		t.Fatalf("missing workspace err = %v, want ErrMissingWorkspace", err)
	}
	if _, err := StartPreviewListener(context.Background(), PreviewConfig{Workspace: ws, JobRegistry: jobs}); !errors.Is(err, ErrMissingObserver) {
		t.Fatalf("missing observer err = %v, want ErrMissingObserver", err)
	}
	if _, err := StartPreviewListener(context.Background(), PreviewConfig{Workspace: ws, Observer: obs}); !errors.Is(err, ErrMissingJobRegistry) {
		t.Fatalf("missing job registry err = %v, want ErrMissingJobRegistry", err)
	}

	pl, err := StartPreviewListener(context.Background(), PreviewConfig{
		ExecutionID:      "exec-preview-fc",
		JobID:            "job-fc-1",
		Args:             []string{"0"},
		Workspace:        ws,
		Observer:         obs,
		JobRegistry:      jobs,
		MaxResponseBytes: 128,
	})
	testutil.FailErr(t, "start bounded preview", err)
	defer func() { _ = pl.Stop(context.Background()) }()

	// Allowed small file succeeds.
	respOK, err := http.Get("http://" + pl.Address() + "/ok.txt")
	testutil.FailErr(t, "GET ok.txt", err)
	_ = respOK.Body.Close()
	if respOK.StatusCode != http.StatusOK {
		t.Fatalf("GET ok.txt status = %d, want 200", respOK.StatusCode)
	}

	// Oversized file is rejected with 413 Request Entity Too Large.
	respLarge, err := http.Get("http://" + pl.Address() + "/large.txt")
	testutil.FailErr(t, "GET large.txt", err)
	_ = respLarge.Body.Close()
	if respLarge.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("GET large.txt status = %d, want 413", respLarge.StatusCode)
	}

	// Per-request Observer.Admit(EffectFilesystemRead) blocks secret.txt with 403.
	respSecret, err := http.Get("http://" + pl.Address() + "/secret.txt")
	testutil.FailErr(t, "GET secret.txt", err)
	_ = respSecret.Body.Close()
	if respSecret.StatusCode != http.StatusForbidden {
		t.Fatalf("GET secret.txt status = %d, want 403", respSecret.StatusCode)
	}
}
