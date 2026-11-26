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
	Mode           ExecutionMode
	Code           string
	Script         string
	Module         string
	CLI            string
	CWD            string
	Args           []string
	PackagesMount  string
	ScratchMount   string
	HTTPBridgePath string
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
		Mode           string   `json:"mode"`
		Code           string   `json:"code"`
		Script         string   `json:"script"`
		Module         string   `json:"module"`
		CLI            string   `json:"cli"`
		CWD            string   `json:"cwd"`
		Args           []string `json:"args"`
		PackagesMount  string   `json:"packages_mount"`
		ScratchMount   string   `json:"scratch_mount"`
		HTTPBridgePath string   `json:"http_bridge_path,omitempty"`
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
		HTTPBridgePath: cfg.HTTPBridgePath,
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

const trustedBootstrapTemplate = `import atexit, base64, builtins, io, json, os, runpy, sys

def _pw_install_http_bridge(bridge_path):
    _orig_import = builtins.__import__
    _patched = [False]

    def _call_bridge(payload_obj):
        raw_req = json.dumps(payload_obj).encode("utf-8")
        with open(bridge_path, "r+b", buffering=0) as _bfd:
            _bfd.write(raw_req)
            _bfd.seek(0)
            raw_resp = _bfd.read()
        if not raw_resp:
            return {"error": "empty host HTTP bridge response"}
        return json.loads(raw_resp.decode("utf-8"))

    def _patch_requests(req_mod):
        from requests.adapters import BaseAdapter
        from requests.exceptions import RequestException
        from requests.models import Response
        from requests.structures import CaseInsensitiveDict
        from requests.utils import get_encoding_from_headers

        class _PWStreamRaw(io.RawIOBase):
            def __init__(self, handle_id):
                self._handle_id = handle_id
                self._buf = bytearray()
                self._eof = False

            def readable(self):
                return True

            def read(self, size=-1):
                if size is None or size < 0:
                    chunks = []
                    if self._buf:
                        chunks.append(bytes(self._buf))
                        self._buf.clear()
                    while not self._eof:
                        res = _call_bridge({"action": "read_chunk", "stream_handle_id": self._handle_id, "max_bytes": 32768})
                        if res.get("error"):
                            raise RequestException(res["error"])
                        if res.get("data_base64"):
                            chunks.append(base64.b64decode(res["data_base64"]))
                        if res.get("eof"):
                            self._eof = True
                    return b"".join(chunks)
                while len(self._buf) < size and not self._eof:
                    res = _call_bridge({"action": "read_chunk", "stream_handle_id": self._handle_id, "max_bytes": max(size - len(self._buf), 4096)})
                    if res.get("error"):
                        raise RequestException(res["error"])
                    if res.get("data_base64"):
                        self._buf.extend(base64.b64decode(res["data_base64"]))
                    if res.get("eof"):
                        self._eof = True
                out = bytes(self._buf[:size])
                del self._buf[:size]
                return out

            def close(self):
                if not self.closed:
                    try:
                        _call_bridge({"action": "close_stream", "stream_handle_id": self._handle_id})
                    except Exception:
                        pass
                    super().close()

        class _PWHostMediatedAdapter(BaseAdapter):
            def send(self, request, stream=False, timeout=None, verify=True, cert=None, proxies=None):
                payload = {
                    "method": request.method or "GET",
                    "url": request.url,
                    "headers": {k: str(v) for k, v in (request.headers or {}).items()},
                    "stream": bool(stream),
                }
                if request.body is not None:
                    if isinstance(request.body, bytes):
                        raw_body = request.body
                    elif isinstance(request.body, str):
                        raw_body = request.body.encode("utf-8")
                    elif hasattr(request.body, "read"):
                        b = request.body.read()
                        raw_body = b.encode("utf-8") if isinstance(b, str) else b
                    else:
                        raw_body = bytes(request.body)
                    if raw_body:
                        payload["body_base64"] = base64.b64encode(raw_body).decode("ascii")
                if isinstance(timeout, (int, float)) and timeout > 0:
                    payload["timeout_ms"] = int(timeout * 1000)
                elif isinstance(timeout, tuple) and timeout:
                    vals = [t for t in timeout if isinstance(t, (int, float)) and t > 0]
                    if vals:
                        payload["timeout_ms"] = int(max(vals) * 1000)

                resp_data = _call_bridge(payload)
                if resp_data.get("stop_code") or resp_data.get("error"):
                    raise RequestException(resp_data.get("error") or resp_data.get("stop_code"))

                resp = Response()
                resp.status_code = int(resp_data.get("status_code") or 0)
                resp.url = resp_data.get("final_url") or request.url
                resp.headers = CaseInsensitiveDict(resp_data.get("headers") or {})
                resp.encoding = get_encoding_from_headers(resp.headers)
                resp.request = request
                history = []
                for hop in resp_data.get("redirects") or []:
                    h = Response()
                    h.status_code = int(hop.get("status_code") or 302)
                    h.url = hop.get("from_url") or ""
                    h.request = request
                    h._content = b""
                    h._content_consumed = True
                    history.append(h)
                resp.history = history

                if stream and resp_data.get("stream_handle_id"):
                    resp.raw = _PWStreamRaw(resp_data["stream_handle_id"])
                    resp._content = False
                    resp._content_consumed = False
                else:
                    body_bytes = base64.b64decode(resp_data["body_base64"]) if resp_data.get("body_base64") else b""
                    resp.raw = io.BytesIO(body_bytes)
                    resp._content = body_bytes
                    resp._content_consumed = True
                return resp

            def close(self):
                pass

        _orig_init = req_mod.Session.__init__

        def _pw_session_init(self, *args, **kwargs):
            _orig_init(self, *args, **kwargs)
            adapter = _PWHostMediatedAdapter()
            self.mount("http://", adapter)
            self.mount("https://", adapter)

        req_mod.Session.__init__ = _pw_session_init

    def _pw_import(name, globals=None, locals=None, fromlist=(), level=0):
        mod = _orig_import(name, globals, locals, fromlist, level)
        if not _patched[0] and (name == "requests" or name.startswith("requests.")):
            req_mod = sys.modules.get("requests")
            if req_mod is not None and hasattr(req_mod, "Session"):
                _patched[0] = True
                _patch_requests(req_mod)
        return mod

    builtins.__import__ = _pw_import

def _pw_pytest_args(args):
    items = list(args)
    has_capture = any(
        a == "-s" or a == "--capture" or a.startswith("--capture=")
        for a in items
    )
    has_no_fault = any(a == "no:faulthandler" or a == "-pno:faulthandler" for a in items)
    has_no_cache = any(a == "no:cacheprovider" or a == "-pno:cacheprovider" for a in items)
    prefix = []
    if not has_capture:
        prefix.append("--capture=sys")
    if not has_no_fault:
        prefix.extend(["-p", "no:faulthandler"])
    if not has_no_cache:
        prefix.extend(["-p", "no:cacheprovider"])
    return prefix + items

def _pw_install_pytest_defaults():
    _orig_import = builtins.__import__
    _patched = [False]

    def _patch_pytest(pytest_mod):
        _orig_main = pytest_mod.main

        def _pw_main(args=None, plugins=None):
            if args is None:
                norm = _pw_pytest_args([])
            elif isinstance(args, (str, bytes, os.PathLike)):
                norm = _pw_pytest_args([os.fspath(args)])
            else:
                norm = _pw_pytest_args(list(args))
            return _orig_main(norm, plugins=plugins)

        pytest_mod.main = _pw_main

    def _pw_import(name, globals=None, locals=None, fromlist=(), level=0):
        mod = _orig_import(name, globals, locals, fromlist, level)
        if not _patched[0] and (name == "pytest" or name.startswith("pytest.")):
            pytest_mod = sys.modules.get("pytest")
            if pytest_mod is not None and hasattr(pytest_mod, "main"):
                _patched[0] = True
                _patch_pytest(pytest_mod)
        return mod

    builtins.__import__ = _pw_import

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
        if mod_name == "pytest":
            args = _pw_pytest_args(args)
        sys.argv = [mod_name] + args
    elif mode == "stdin":
        sys.path[:] = [cwd] + base_path
        sys.argv = ["-"] + args
    else:
        sys.path[:] = [cwd] + base_path
        sys.argv = ["-c"] + args

    if cfg.get("http_bridge_path"):
        _pw_install_http_bridge(cfg["http_bridge_path"])
    _pw_install_pytest_defaults()

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
