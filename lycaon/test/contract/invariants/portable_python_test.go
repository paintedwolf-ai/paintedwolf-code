package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
)

// portablePythonHTTPBase is a minimal type-checked fixture matching the
// production guest HTTP dispatch entry points and approved door, plus a
// legitimate staging artifact fetcher unreachable from guest dispatch.
var portablePythonHTTPBase = map[string]string{
	"lycaon/internal/pythonadmission/http_transport.go": `package pythonadmission

import (
	"context"
	"net/http"
)

type HTTPTransport struct{ client *http.Client }

func NewHTTPTransport(c *http.Client) *HTTPTransport { return &HTTPTransport{client: c} }

func (t *HTTPTransport) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return t.client.Do(req)
}

type GuestHTTPAdapter struct{ tr *HTTPTransport }

func (a *GuestHTTPAdapter) HandleJSON(ctx context.Context, raw []byte) ([]byte, error) {
	_, _ = a.tr.Do(ctx, nil)
	return nil, nil
}

func (a *GuestHTTPAdapter) ReadStreamChunk(ctx context.Context, id string, n int) ([]byte, error) {
	return nil, nil
}
`,
	"lycaon/internal/portablepython/bridge.go": `package portablepython

import (
	"context"
	"github.com/lycaon/lycaon/internal/pythonadmission"
)

type Service struct{ adapter *pythonadmission.GuestHTTPAdapter }

func (s *Service) ExecuteTool(ctx context.Context) error {
	_, err := s.adapter.HandleJSON(ctx, nil)
	return err
}

type GuestHTTPBridgeFS struct{ adapter *pythonadmission.GuestHTTPAdapter }

func NewGuestHTTPBridgeFS(a *pythonadmission.GuestHTTPAdapter) *GuestHTTPBridgeFS {
	return &GuestHTTPBridgeFS{adapter: a}
}

func (b *GuestHTTPBridgeFS) OpenFile(p string) *httpBridgeFile {
	return &httpBridgeFile{parent: b}
}

type httpBridgeFile struct{ parent *GuestHTTPBridgeFS }

func (f *httpBridgeFile) Read(p []byte) (int, error) {
	_, err := f.parent.adapter.HandleJSON(context.Background(), p)
	return 0, err
}

func (f *httpBridgeFile) Seek(off int64, whence int) (int64, error) {
	_, err := f.parent.adapter.ReadStreamChunk(context.Background(), "h", 16)
	return 0, err
}
`,
	// Legitimate staging artifact fetch — unreachable from guest dispatch.
	"lycaon/internal/pythonruntime/engine_stage.go": `package pythonruntime

import (
	"context"
	"net/http"
)

func FetchEngineArtifact(ctx context.Context, url string) (*http.Response, error) {
	c := &http.Client{}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	return c.Do(req)
}
`,
}

// Rule A fixture controls:
//   - valid delegated call + unreachable staging fetch passes
//   - direct net/http call from guest dispatch fires
//   - aliased helper call (and raw socket dial) reachable from guest dispatch fires
//   - renaming helper, relocating to another file in package, or aliasing import preserves verdict
func TestPortablePythonGuestHTTPBridgeRule(t *testing.T) {
	t.Parallel()
	r := rule(t, forbiddencalls.Family, "portable-python-guest-http-uses-mediated-bridge")

	t.Run("valid delegated call and unreachable staging fetch pass", func(t *testing.T) {
		t.Parallel()
		expect(t, invariants.Evaluate(fixture(t, portablePythonHTTPBase), r), passes, 0)
	})

	t.Run("direct raw http call in guest dispatch fires", func(t *testing.T) {
		t.Parallel()
		files := with(portablePythonHTTPBase, map[string]string{
			"lycaon/internal/portablepython/bridge.go": `package portablepython

import (
	"context"
	"net/http"
	"github.com/lycaon/lycaon/internal/pythonadmission"
)

type Service struct{ adapter *pythonadmission.GuestHTTPAdapter }

func (s *Service) ExecuteTool(ctx context.Context) error {
	_, err := http.Get("https://example.com")
	return err
}

type GuestHTTPBridgeFS struct{ adapter *pythonadmission.GuestHTTPAdapter }

func NewGuestHTTPBridgeFS(a *pythonadmission.GuestHTTPAdapter) *GuestHTTPBridgeFS { return &GuestHTTPBridgeFS{adapter: a} }
func (b *GuestHTTPBridgeFS) OpenFile(p string) *httpBridgeFile { return &httpBridgeFile{parent: b} }
type httpBridgeFile struct{ parent *GuestHTTPBridgeFS }
func (f *httpBridgeFile) Read(p []byte) (int, error) { return 0, nil }
func (f *httpBridgeFile) Seek(off int64, whence int) (int64, error) { return 0, nil }
`,
		})
		expect(t, invariants.Evaluate(fixture(t, files), r), violates, 1)
	})

	t.Run("aliased helper call and relocate-rename-alias invariance", func(t *testing.T) {
		t.Parallel()
		// Variant 1: helper in same file with aliased import.
		v1 := with(portablePythonHTTPBase, map[string]string{
			"lycaon/internal/portablepython/helper_a.go": `package portablepython

import rawnet "net"

func dialOutside() {
	_, _ = rawnet.Dial("tcp", "127.0.0.1:443")
}
`,
			"lycaon/internal/portablepython/bridge.go": `package portablepython

import (
	"context"
	"github.com/lycaon/lycaon/internal/pythonadmission"
)

type Service struct{ adapter *pythonadmission.GuestHTTPAdapter }

func (s *Service) ExecuteTool(ctx context.Context) error {
	dialOutside()
	return nil
}

type GuestHTTPBridgeFS struct{ adapter *pythonadmission.GuestHTTPAdapter }

func NewGuestHTTPBridgeFS(a *pythonadmission.GuestHTTPAdapter) *GuestHTTPBridgeFS { return &GuestHTTPBridgeFS{adapter: a} }
func (b *GuestHTTPBridgeFS) OpenFile(p string) *httpBridgeFile { return &httpBridgeFile{parent: b} }
type httpBridgeFile struct{ parent *GuestHTTPBridgeFS }
func (f *httpBridgeFile) Read(p []byte) (int, error) { return 0, nil }
func (f *httpBridgeFile) Seek(off int64, whence int) (int64, error) { return 0, nil }
`,
		})
		expect(t, invariants.Evaluate(fixture(t, v1), r), violates, 1)

		// Variant 2: renamed helper, relocated to a different file, different import alias -> identical verdict!
		v2 := with(portablePythonHTTPBase, map[string]string{
			"lycaon/internal/portablepython/relocated_net_util.go": `package portablepython

import sockpkg "net"

func completelyUnrelatedHelperName() {
	_, _ = sockpkg.Dial("tcp", "127.0.0.1:443")
}
`,
			"lycaon/internal/portablepython/bridge.go": `package portablepython

import (
	"context"
	"github.com/lycaon/lycaon/internal/pythonadmission"
)

type Service struct{ adapter *pythonadmission.GuestHTTPAdapter }

func (s *Service) ExecuteTool(ctx context.Context) error {
	completelyUnrelatedHelperName()
	return nil
}

type GuestHTTPBridgeFS struct{ adapter *pythonadmission.GuestHTTPAdapter }

func NewGuestHTTPBridgeFS(a *pythonadmission.GuestHTTPAdapter) *GuestHTTPBridgeFS { return &GuestHTTPBridgeFS{adapter: a} }
func (b *GuestHTTPBridgeFS) OpenFile(p string) *httpBridgeFile { return &httpBridgeFile{parent: b} }
type httpBridgeFile struct{ parent *GuestHTTPBridgeFS }
func (f *httpBridgeFile) Read(p []byte) (int, error) { return 0, nil }
func (f *httpBridgeFile) Seek(off int64, whence int) (int64, error) { return 0, nil }
`,
		})
		expect(t, invariants.Evaluate(fixture(t, v2), r), violates, 1)
	})

	t.Run("disappearing single live root fails closed without silent shrink", func(t *testing.T) {
		t.Parallel()
		// Keep Service.ExecuteTool, GuestHTTPBridgeFS.OpenFile, httpBridgeFile.Read,
		// and GuestHTTPAdapter.HandleJSON intact, but rename httpBridgeFile.Seek
		// so one live root disappears while all others remain.
		shrunkBridge := with(portablePythonHTTPBase, map[string]string{
			"lycaon/internal/portablepython/bridge.go": `package portablepython

import (
	"context"
	"github.com/lycaon/lycaon/internal/pythonadmission"
)

type Service struct{ adapter *pythonadmission.GuestHTTPAdapter }

func (s *Service) ExecuteTool(ctx context.Context) error {
	_, err := s.adapter.HandleJSON(ctx, nil)
	return err
}

type GuestHTTPBridgeFS struct{ adapter *pythonadmission.GuestHTTPAdapter }

func NewGuestHTTPBridgeFS(a *pythonadmission.GuestHTTPAdapter) *GuestHTTPBridgeFS {
	return &GuestHTTPBridgeFS{adapter: a}
}

func (b *GuestHTTPBridgeFS) OpenFile(p string) *httpBridgeFile {
	return &httpBridgeFile{parent: b}
}

type httpBridgeFile struct{ parent *GuestHTTPBridgeFS }

func (f *httpBridgeFile) Read(p []byte) (int, error) {
	_, err := f.parent.adapter.HandleJSON(context.Background(), p)
	return 0, err
}

// Seek renamed -> must fail closed rather than silently shrinking coverage.
func (f *httpBridgeFile) Reposition(off int64, whence int) (int64, error) { return 0, nil }
`,
		})
		expect(t, invariants.Evaluate(fixture(t, shrunkBridge), r), violates, 1)
	})
}

// wazeroStub stands in for the wazero packages in mount-rule fixtures so
// go/types resolves exact interface and method symbols without network access.
var wazeroStub = map[string]string{
	"lycaon/wazerostub/go.mod": "module github.com/tetratelabs/wazero\n\ngo 1.26\n",
	"lycaon/wazerostub/wazero.go": `package wazero

type FSConfig interface {
	WithDirMount(dir, guestPath string) FSConfig
	WithReadOnlyDirMount(dir, guestPath string) FSConfig
}

func NewFSConfig() FSConfig { return nil }
`,
	"lycaon/wazerostub/experimental/sys/sys.go": `package sys

type FS interface{}
`,
	"lycaon/wazerostub/experimental/sysfs/sysfs.go": `package sysfs

import (
	"github.com/tetratelabs/wazero"
	expsys "github.com/tetratelabs/wazero/experimental/sys"
)

type FSConfig interface {
	wazero.FSConfig
	WithSysFSMount(fs expsys.FS, guestPath string) wazero.FSConfig
}
`,
	"lycaon/go.mod": fixtureModule + "\nrequire github.com/tetratelabs/wazero v1.0.0\n\nreplace github.com/tetratelabs/wazero => ./wazerostub\n",
	"lycaon/internal/portablepython/placeholder.go": "package portablepython\n",
	"lycaon/internal/pythonruntime/mounts.go": `package pythonruntime

import (
	"github.com/tetratelabs/wazero"
	expsys "github.com/tetratelabs/wazero/experimental/sys"
	expsysfs "github.com/tetratelabs/wazero/experimental/sysfs"
)

const (
	DefaultGuestWorkspaceMount = "/workspace"
	DefaultGuestPackagesMount  = "/packages"
	DefaultGuestScratchMount   = "/tmp"
)

type WorkspaceSysFS struct{}

func NewWorkspaceSysFS() *WorkspaceSysFS { return &WorkspaceSysFS{} }

type hostDirSysFS struct{ root string; ro bool }

func newHostDirSysFS(root string, ro bool) *hostDirSysFS { return &hostDirSysFS{root: root, ro: ro} }

type nativePreopen struct {
	guestPath string
	fs        expsys.FS
}

type WazeroRuntime struct{ workspaceMount string }

func (r *WazeroRuntime) buildModuleConfig(stdlib, pkgs, scratch string) wazero.FSConfig {
	cfg := wazero.NewFSConfig()
	cfg = cfg.WithReadOnlyDirMount(stdlib, "/usr/local/lib")
	cfg = cfg.WithReadOnlyDirMount(pkgs, DefaultGuestPackagesMount)
	cfg = cfg.WithDirMount(scratch, DefaultGuestScratchMount)
	if sc, ok := cfg.(expsysfs.FSConfig); ok {
		cfg = sc.WithSysFSMount(NewWorkspaceSysFS(), r.workspaceMount)
	}
	return cfg
}

type NativeEngineRuntime struct{ workspaceMount string }

func (r *NativeEngineRuntime) buildPreopens(stdlib, pkgs, scratch string) []nativePreopen {
	return []nativePreopen{
		{guestPath: "/usr/local/lib", fs: newHostDirSysFS(stdlib, true)},
		{guestPath: DefaultGuestPackagesMount, fs: newHostDirSysFS(pkgs, true)},
		{guestPath: DefaultGuestScratchMount, fs: newHostDirSysFS(scratch, false)},
		{guestPath: r.workspaceMount, fs: NewWorkspaceSysFS()},
	}
}
`,
}

// Rule B fixture controls:
//   - valid trusted mounts + valid mediated workspace binding on both backends pass
//   - native-aot workspace binding through a helper to newHostDirSysFS fires
//   - wazero workspace binding via WithDirMount fires
//   - renaming helper, relocating file, or aliasing wazero import preserves verdict
func TestPortablePythonWorkspaceMountsRule(t *testing.T) {
	t.Parallel()
	r := rule(t, forbiddencalls.Family, "portable-python-workspace-mounts-use-mediated-fs")

	t.Run("valid trusted mounts and mediated workspace pass on both backends", func(t *testing.T) {
		t.Parallel()
		expect(t, invariants.Evaluate(fixture(t, wazeroStub), r), passes, 0)
	})

	t.Run("native-aot workspace binding through helper to raw adapter fires and survives rename-relocate", func(t *testing.T) {
		t.Parallel()
		// Variant 1: helper in helper_a.go returning interface expsys.FS.
		v1 := with(wazeroStub, map[string]string{
			"lycaon/internal/pythonruntime/helper_a.go": `package pythonruntime

import expsys "github.com/tetratelabs/wazero/experimental/sys"

func wrapRawWorkspace(dir string) expsys.FS {
	return newHostDirSysFS(dir, false)
}

func (r *NativeEngineRuntime) badBind(dir string) nativePreopen {
	return nativePreopen{guestPath: r.workspaceMount, fs: wrapRawWorkspace(dir)}
}
`,
		})
		expect(t, invariants.Evaluate(fixture(t, v1), r), violates, 1)

		// Variant 2: renamed helper, relocated file, aliased import -> identical verdict!
		v2 := with(wazeroStub, map[string]string{
			"lycaon/internal/pythonruntime/relocated_fs_adapter.go": `package pythonruntime

import sysalias "github.com/tetratelabs/wazero/experimental/sys"

func unrelatedAdapterFactory(dir string) sysalias.FS {
	return newHostDirSysFS(dir, false)
}

func (r *NativeEngineRuntime) anotherBind(dir string) nativePreopen {
	f := unrelatedAdapterFactory(dir)
	return nativePreopen{guestPath: r.workspaceMount, fs: f}
}
`,
		})
		expect(t, invariants.Evaluate(fixture(t, v2), r), violates, 1)
	})

	t.Run("wazero raw WithDirMount on workspace fires even with aliased import", func(t *testing.T) {
		t.Parallel()
		files := with(wazeroStub, map[string]string{
			"lycaon/internal/pythonruntime/bad_wazero.go": `package pythonruntime

import wz "github.com/tetratelabs/wazero"

func badWazeroMount(wsDir string) wz.FSConfig {
	return wz.NewFSConfig().WithDirMount(wsDir, DefaultGuestWorkspaceMount)
}
`,
		})
		expect(t, invariants.Evaluate(fixture(t, files), r), violates, 1)
	})
}
