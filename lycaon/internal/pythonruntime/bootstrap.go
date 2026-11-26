package pythonruntime

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"
)

const (
	// DefaultGuestWorkspaceMount is the default WASI mount point for the
	// mediated workspace adapter.
	DefaultGuestWorkspaceMount = "/workspace"
	// DefaultGuestPackagesMount is the immutable bundled packages mount point.
	DefaultGuestPackagesMount = "/packages"
	// DefaultGuestScratchMount is the program-private writable temporary mount.
	DefaultGuestScratchMount = "/tmp"
	// InternalCompletionMount is the host-only mount point that receives the
	// trusted clean-completion nonce after normal Python finalization.
	InternalCompletionMount = "/.pw_internal"
	// InternalCompletionFile is the filename inside InternalCompletionMount.
	InternalCompletionFile = "complete"
)

// BootstrapConfig configures the isolated CPython bootstrap preamble.
type BootstrapConfig struct {
	Mode            ExecutionMode
	Code            string
	Script          string
	Module          string
	CLI             string
	CWD             string
	Args            []string
	PackagesMount   string
	ScratchMount    string
	CompletionNonce string
}

// GenerateCompletionNonce returns a fresh 16-byte hex nonce for trusted
// clean-completion verification.
func GenerateCompletionNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// BuildBootstrapArgv returns the isolated CPython argv (`python3 -I -B -c <bootstrap>`)
// that initializes cwd, sys.path, scratch TMPDIR, executes the requested mode,
// flushes streams, runs atexit finalizers, and writes the completion nonce to
// the host-only completion channel.
func BuildBootstrapArgv(cfg BootstrapConfig) ([]string, error) {
	src, err := TrustedBootstrapSource(cfg)
	if err != nil {
		return nil, err
	}
	return []string{"python3", "-I", "-B", "-c", src}, nil
}

// TrustedBootstrapSource renders the trusted Python bootstrap preamble for cfg.
func TrustedBootstrapSource(cfg BootstrapConfig) (string, error) {
	inv := Invocation{
		Mode:   cfg.Mode,
		Code:   cfg.Code,
		Script: cfg.Script,
		Module: cfg.Module,
		CLI:    cfg.CLI,
		Args:   cfg.Args,
	}
	if err := inv.Validate(); err != nil {
		return "", err
	}

	cwd := cfg.CWD
	if strings.TrimSpace(cwd) == "" {
		cwd = DefaultGuestWorkspaceMount
	}
	pkgMount := cfg.PackagesMount
	if strings.TrimSpace(pkgMount) == "" {
		pkgMount = DefaultGuestPackagesMount
	}
	scratch := cfg.ScratchMount
	if strings.TrimSpace(scratch) == "" {
		scratch = DefaultGuestScratchMount
	}

	payload := struct {
		Mode           string   `json:"mode"`
		Code           string   `json:"code"`
		Script         string   `json:"script"`
		Module         string   `json:"module"`
		CLI            string   `json:"cli"`
		CWD            string   `json:"cwd"`
		Args           []string `json:"args"`
		PackagesMount  string   `json:"packages_mount"`
		ScratchMount   string   `json:"scratch_mount"`
		CompletionPath string   `json:"completion_path"`
		Nonce          string   `json:"nonce"`
	}{
		Mode:           string(cfg.Mode),
		Code:           cfg.Code,
		Script:         cfg.Script,
		Module:         cfg.Module,
		CLI:            cfg.CLI,
		CWD:            cwd,
		Args:           cfg.Args,
		PackagesMount:  pkgMount,
		ScratchMount:   scratch,
		CompletionPath: InternalCompletionMount + "/" + InternalCompletionFile,
		Nonce:          cfg.CompletionNonce,
	}
	if payload.Args == nil {
		payload.Args = []string{}
	}
	rawCfg, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode bootstrap config: %w", err)
	}

	return fmt.Sprintf(trustedBootstrapTemplate, string(rawCfg)), nil
}

const trustedBootstrapTemplate = `import atexit, json, os, runpy, sys

def _pw_bootstrap():
    cfg = json.loads(%q)
    for _k in ("PYTHONPATH", "PYTHONUSERBASE", "PYTHONSTARTUP"):
        os.environ.pop(_k, None)
    scratch = cfg["scratch_mount"]
    os.environ["TMPDIR"] = scratch
    os.environ["TEMP"] = scratch
    os.environ["TMP"] = scratch
    os.environ["PYTHONDONTWRITEBYTECODE"] = "1"
    sys.dont_write_bytecode = True

    cwd = cfg["cwd"]
    os.chdir(cwd)

    mode = cfg["mode"]
    args = list(cfg["args"])
    pkg_mount = cfg["packages_mount"]

    base_path = [p for p in sys.path if p and p != ""]
    if pkg_mount and pkg_mount not in base_path:
        base_path.append(pkg_mount)

    if mode == "script":
        script_path = cfg["script"]
        script_dir = os.path.dirname(os.path.abspath(script_path))
        sys.path[:] = [script_dir] + base_path
        sys.argv = [script_path] + args
    elif mode in ("module", "cli"):
        sys.path[:] = [cwd] + base_path
        mod_name = cfg["module"] if mode == "module" else cfg["cli"]
        sys.argv = [mod_name] + args
    elif mode == "stdin":
        sys.path[:] = [cwd] + base_path
        sys.argv = ["-"] + args
    else:
        sys.path[:] = [cwd] + base_path
        sys.argv = ["-c"] + args

    exit_code = 0
    try:
        if mode == "code":
            ns = {"__name__": "__main__", "__file__": "<string>"}
            exec(compile(cfg["code"], "<string>", "exec"), ns)
        elif mode == "stdin":
            src = sys.stdin.read()
            ns = {"__name__": "__main__", "__file__": "<stdin>"}
            exec(compile(src, "<stdin>", "exec"), ns)
        elif mode == "script":
            script_path = cfg["script"]
            with open(script_path, "rb") as _f:
                src = _f.read()
            ns = {"__name__": "__main__", "__file__": script_path}
            exec(compile(src, script_path, "exec"), ns)
        elif mode in ("module", "cli"):
            mod_name = cfg["module"] if mode == "module" else cfg["cli"]
            runpy.run_module(mod_name, run_name="__main__", alter_sys=True)
    except SystemExit as exc:
        if exc.code is None or exc.code == 0:
            exit_code = 0
        else:
            raise

    atexit._run_exitfuncs()
    sys.stdout.flush()
    sys.stderr.flush()

    nonce = cfg.get("nonce", "")
    if exit_code == 0 and nonce:
        with open(cfg["completion_path"], "wb") as _cf:
            _cf.write(nonce.encode("ascii"))

_pw_bootstrap()
`

// CompletionMarkerFS is a host-only experimental/sys.FS mounted at
// InternalCompletionMount. Normal bootstrap finalization writes the per-
// invocation nonce to "complete"; os._exit(0), uncaught exceptions, traps, or
// terminal stops never record completion.
type CompletionMarkerFS struct {
	experimentalsys.UnimplementedFS

	nonce     []byte
	latch     *StopLatch
	completed atomic.Bool
}

// NewCompletionMarkerFS creates a host-only completion marker FS bound to
// nonce and latch.
func NewCompletionMarkerFS(nonce string, latch *StopLatch) *CompletionMarkerFS {
	return &CompletionMarkerFS{
		nonce: []byte(nonce),
		latch: latch,
	}
}

// Completed reports whether the exact per-invocation completion nonce was
// delivered before any terminal stop.
func (c *CompletionMarkerFS) Completed() bool {
	return c.completed.Load()
}

func (c *CompletionMarkerFS) OpenFile(p string, _ experimentalsys.Oflag, _ fs.FileMode) (experimentalsys.File, experimentalsys.Errno) {
	if c.latch != nil && c.latch.Stopped() {
		return nil, experimentalsys.EACCES
	}
	rel := cleanGuestRelPath(p)
	switch rel {
	case ".":
		return &completionDirFile{}, 0
	case InternalCompletionFile:
		return &completionMarkerFile{parent: c}, 0
	default:
		return nil, experimentalsys.ENOENT
	}
}

func (c *CompletionMarkerFS) Stat(p string) (sys.Stat_t, experimentalsys.Errno) {
	rel := cleanGuestRelPath(p)
	switch rel {
	case ".":
		return sys.Stat_t{Dev: 3, Ino: 1, Mode: fs.ModeDir | 0o700, Nlink: 1}, 0
	case InternalCompletionFile:
		return sys.Stat_t{Dev: 3, Ino: 2, Mode: 0o200, Nlink: 1}, 0
	default:
		return sys.Stat_t{}, experimentalsys.ENOENT
	}
}

func (c *CompletionMarkerFS) Lstat(p string) (sys.Stat_t, experimentalsys.Errno) {
	return c.Stat(p)
}

type completionDirFile struct {
	experimentalsys.DirFile
	experimentalsys.UnimplementedFile
}

func (completionDirFile) Dev() (uint64, experimentalsys.Errno)    { return 3, 0 }
func (completionDirFile) Ino() (sys.Inode, experimentalsys.Errno) { return 1, 0 }
func (completionDirFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	return sys.Stat_t{Dev: 3, Ino: 1, Mode: fs.ModeDir | 0o700, Nlink: 1}, 0
}
func (completionDirFile) Close() experimentalsys.Errno { return 0 }

type completionMarkerFile struct {
	experimentalsys.UnimplementedFile
	parent *CompletionMarkerFS
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (f *completionMarkerFile) Dev() (uint64, experimentalsys.Errno)    { return 3, 0 }
func (f *completionMarkerFile) Ino() (sys.Inode, experimentalsys.Errno) { return 2, 0 }
func (f *completionMarkerFile) IsDir() (bool, experimentalsys.Errno)    { return false, 0 }
func (f *completionMarkerFile) IsAppend() bool                          { return false }
func (f *completionMarkerFile) SetAppend(bool) experimentalsys.Errno    { return 0 }
func (f *completionMarkerFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	return sys.Stat_t{Dev: 3, Ino: 2, Mode: 0o200, Nlink: 1}, 0
}
func (f *completionMarkerFile) Write(p []byte) (int, experimentalsys.Errno) {
	if f.parent.latch != nil && f.parent.latch.Stopped() {
		return 0, experimentalsys.EACCES
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.buf.Len()+len(p) > len(f.parent.nonce)+64 {
		return 0, experimentalsys.EINVAL
	}
	f.buf.Write(p)
	if len(f.parent.nonce) > 0 && bytes.Equal(f.buf.Bytes(), f.parent.nonce) {
		f.parent.completed.Store(true)
	}
	return len(p), 0
}
func (f *completionMarkerFile) Truncate(int64) experimentalsys.Errno { return 0 }
func (f *completionMarkerFile) Sync() experimentalsys.Errno          { return 0 }
func (f *completionMarkerFile) Datasync() experimentalsys.Errno      { return 0 }
func (f *completionMarkerFile) Close() experimentalsys.Errno         { return 0 }
