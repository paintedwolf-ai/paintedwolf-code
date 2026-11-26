package pythonruntime

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// DefaultGuestWorkspaceMount is the default WASI mount point for the
	// mediated workspace adapter.
	DefaultGuestWorkspaceMount = "/workspace"
	// DefaultGuestPackagesMount is the immutable bundled packages mount point.
	DefaultGuestPackagesMount = "/packages"
	// DefaultGuestScratchMount is the program-private writable temporary mount.
	DefaultGuestScratchMount = "/tmp"
)

// BootstrapConfig configures the isolated CPython bootstrap preamble.
// Clean completion is established strictly by host/embedding lifecycle
// authority (clean _start return without proc_exit, zero stream errors, and
// zero unclosed workspace write descriptors) — no guest-visible completion
// nonce or marker is used.
type BootstrapConfig struct {
	Mode          ExecutionMode
	Code          string
	Script        string
	Module        string
	CLI           string
	CWD           string
	Args          []string
	PackagesMount string
	ScratchMount  string
}

// BuildBootstrapArgv returns the isolated CPython argv (`python3 -I -B -c <bootstrap>`)
// that initializes cwd, sys.path, scratch TMPDIR, executes the requested mode,
// runs atexit finalizers, flushes stdout/stderr, and returns normally through
// Py_FinalizeEx() -> _start without calling proc_exit.
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
		Mode          string   `json:"mode"`
		Code          string   `json:"code"`
		Script        string   `json:"script"`
		Module        string   `json:"module"`
		CLI           string   `json:"cli"`
		CWD           string   `json:"cwd"`
		Args          []string `json:"args"`
		PackagesMount string   `json:"packages_mount"`
		ScratchMount  string   `json:"scratch_mount"`
	}{
		Mode:          string(cfg.Mode),
		Code:          cfg.Code,
		Script:        cfg.Script,
		Module:        cfg.Module,
		CLI:           cfg.CLI,
		CWD:           cwd,
		Args:          cfg.Args,
		PackagesMount: pkgMount,
		ScratchMount:  scratch,
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
        if exc.code is not None and exc.code != 0:
            raise

    atexit._run_exitfuncs()
    sys.stdout.flush()
    sys.stderr.flush()

_pw_bootstrap()
`
