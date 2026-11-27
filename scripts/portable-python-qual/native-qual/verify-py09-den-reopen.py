#!/usr/bin/env python3
"""PY09 Targeted Native Python Activity -> Sidecar Restart -> Real Files/Den UI Reopen Witness.

Runs on macOS arm64 and Linux amd64 with PW_PORTABLE_PYTHON_IMAGE_DIR and
PW_PORTABLE_PYTHON_ENGINE_DIR UNSET:
  1. Starts the real staged `pw` sidecar (Run #1) and runs ONE real `native-aot`
     Python activity (`py09_witness.py`) that writes `py09-den-witness.txt` and
     emits >70 KiB stdout (triggering on-disk retained stdout artifact creation).
  2. Stops `pw` Run #1 and restarts `pw` Run #2 against the EXACT SAME
     `LYCAON_CONFIG_DIR`, `LYCAON_ENGINE_ROOT`, and workspace.
  3. Captures API-scope reopened responses (`api-reopen-*.json`) and copies raw
     persisted receipts (`executions.jsonl`, `admissions.jsonl`, `publications.jsonl`)
     and the raw retained stdout blob from disk.
  4. Starts `lycaon-den` Vite frontend proxied to `pw` Run #2 and drives headless
     Chromium via Playwright (`verify-py09-den-ui.mjs`) to reopen the session in
     Den and open `py09-den-witness.txt` in Files, capturing screenshots + DOM text.
"""

import argparse
import hashlib
import http.server
import json
import os
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

WITNESS_SCRIPT = """import sys
from pathlib import Path

marker = "PY09_DEN_WITNESS_OK_NATIVE_AOT"
content = (
    f"{marker}\\n"
    f"python_version={sys.version.splitlines()[0]}\\n"
    f"platform={sys.platform}\\n"
)
Path("py09-den-witness.txt").write_text(content, encoding="utf-8")

print("PY09_RETAINED_STDOUT_BEGIN", marker)
for i in range(1200):
    print(f"PY09_LINE_{i:04d}: retained output witness payload for Den/Files reopen verification ({marker})")
print("PY09_RETAINED_STDOUT_END", marker)
"""


def sha256_file(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def start_deterministic_provider(req_log_path: Path, tool_result_path: Path):
    state = {"calls": 0}
    lock = threading.Lock()

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            pass

        def do_GET(self):
            if self.path.endswith("/api/tags"):
                resp = {"models": [{"name": "qual-deterministic", "model": "qual-deterministic"}]}
            elif self.path.endswith("/v1/models") or self.path.endswith("/models"):
                resp = {"object": "list", "data": [{"id": "qual-deterministic", "object": "model"}]}
            else:
                resp = {"status": "ok"}
            payload = json.dumps(resp).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def do_POST(self):
            length = int(self.headers.get("Content-Length", "0"))
            raw = self.rfile.read(length) if length > 0 else b"{}"
            try:
                body = json.loads(raw.decode("utf-8"))
            except Exception:
                body = {"raw": raw.decode("utf-8", errors="replace")}

            if self.path.endswith("/api/show"):
                resp = {
                    "capabilities": ["completion", "tools"],
                    "model_info": {
                        "general.context_length": 65536,
                        "qwen3.context_length": 65536,
                    },
                }
                payload = json.dumps(resp).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
                return

            if self.path.endswith("/api/chat"):
                with lock:
                    state["calls"] += 1
                    with req_log_path.open("a", encoding="utf-8") as f:
                        f.write(json.dumps(body, sort_keys=True) + "\n")
                    messages = body.get("messages") or []
                    tool_msgs = [m for m in messages if m.get("role") == "tool"]
                    if not tool_msgs:
                        chunk = {
                            "message": {
                                "role": "assistant",
                                "content": "",
                                "tool_calls": [
                                    {
                                        "function": {
                                            "name": "python",
                                            "arguments": {"script": "py09_witness.py"},
                                        }
                                    }
                                ],
                            },
                            "done": True,
                            "prompt_eval_count": 120,
                            "eval_count": 16,
                        }
                    else:
                        content = str(tool_msgs[-1].get("content") or "")
                        tool_result_path.write_text(content, encoding="utf-8")
                        chunk = {
                            "message": {
                                "role": "assistant",
                                "content": "PY09 witness completed: py09-den-witness.txt written and retained stdout captured.",
                            },
                            "done": True,
                            "prompt_eval_count": 240,
                            "eval_count": 12,
                        }
                payload = (json.dumps(chunk) + "\n").encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/x-ndjson")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
                return

            self.send_response(404)
            self.end_headers()

    port = free_port()
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, port, state


class PwClient:
    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.token = token

    def req_text(self, method: str, path: str, timeout: float = 30.0) -> str:
        r = urllib.request.Request(
            self.base_url + path,
            headers={"Authorization": f"Bearer {self.token}"},
            method=method,
        )
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            return resp.read().decode("utf-8", errors="replace")

    def req(self, method: str, path: str, body=None, timeout: float = 30.0):
        data = None
        headers = {"Authorization": f"Bearer {self.token}"}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        r = urllib.request.Request(
            self.base_url + path, data=data, headers=headers, method=method
        )
        try:
            with urllib.request.urlopen(r, timeout=timeout) as resp:
                raw = resp.read()
                return resp.status, json.loads(raw.decode("utf-8")) if raw else None
        except urllib.error.HTTPError as e:
            err_body = e.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"{method} {path} -> HTTP {e.code}: {err_body}") from e


def wait_for_http(url: str, timeout_s: float = 60.0) -> None:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2.0) as resp:
                if 200 <= resp.status < 500:
                    return
        except Exception:
            pass
        time.sleep(0.5)
    raise RuntimeError(f"endpoint did not become ready at {url} within {timeout_s}s")


def start_pw(binary: Path, engine_root: Path, repo_root: Path, scratch: Path, cfg_dir: Path, port: int, token: str, log_path: Path, llm_debug: Path):
    env = {
        "HOME": str(scratch),
        "PATH": "/usr/bin:/bin",
        "LYCAON_ADDR": f"127.0.0.1:{port}",
        "LYCAON_CONFIG_DIR": str(cfg_dir),
        "LYCAON_ENGINE_ROOT": str(engine_root),
        "LYCAON_API_TOKEN": token,
        "LYCAON_DEV": "1",
        "LYCAON_LLM_DEBUG": "1",
        "LYCAON_LLM_DEBUG_FILE": str(llm_debug),
        "NO_PROXY": "127.0.0.1,localhost",
    }
    doc_core_candidates = sorted((repo_root / "lycaon-den" / "src-tauri" / "binaries").glob("pw-document-core*"))
    if doc_core_candidates:
        env["LYCAON_DOCUMENT_CORE_BINARY"] = str(doc_core_candidates[0])
    assert "PW_PORTABLE_PYTHON_IMAGE_DIR" not in env
    assert "PW_PORTABLE_PYTHON_ENGINE_DIR" not in env
    fh = log_path.open("w", encoding="utf-8")
    proc = subprocess.Popen([str(binary), "serve"], env=env, stdout=fh, stderr=subprocess.STDOUT)
    return proc, fh


def stop_proc(proc: subprocess.Popen, fh=None):
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    if fh is not None:
        fh.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--binary", required=True)
    ap.add_argument("--engine-root", required=True)
    ap.add_argument("--repo-root", required=True)
    ap.add_argument("--out-dir", required=True)
    args = ap.parse_args()

    binary = Path(args.binary).resolve()
    engine_root = Path(args.engine_root).resolve()
    repo_root = Path(args.repo_root).resolve()
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    script_dir = Path(__file__).resolve().parent

    scratch = Path(tempfile.mkdtemp(prefix="py09-den-reopen-")).resolve()
    try:
        ws = scratch / "workspace"
        ws.mkdir(parents=True, exist_ok=True)
        (ws / "py09_witness.py").write_text(WITNESS_SCRIPT, encoding="utf-8")
        cfg_dir = scratch / "cfg"
        cfg_dir.mkdir(parents=True, exist_ok=True)

        token = uuid.uuid4().hex + uuid.uuid4().hex

        # --- Phase 1: Initial pw serve run & native-aot Python activity ---
        prov_srv, prov_port, _ = start_deterministic_provider(
            out_dir / "run1-provider-requests.jsonl",
            out_dir / "run1-tool-result.txt",
        )
        pw1_port = free_port()
        pw1, pw1_fh = start_pw(
            binary, engine_root, repo_root, scratch, cfg_dir,
            pw1_port, token, out_dir / "run1-pw-server.log", out_dir / "run1-llm-requests.jsonl",
        )
        try:
            base1 = f"http://127.0.0.1:{pw1_port}"
            wait_for_http(base1 + "/health", timeout_s=60.0)
            c1 = PwClient(base1, token)
            c1.req("POST", "/v1/providers", {
                "id": "ollama",
                "kind": "ollama",
                "base_url": f"http://127.0.0.1:{prov_port}/v1",
                "requires_api_key": False,
                "models": [{"id": "qual-deterministic"}],
            })
            _, proj = c1.req("POST", "/v1/projects", {
                "name": "py09-den-reopen",
                "roots": [{"path": str(ws), "is_primary": True}],
            })
            proj_id = proj["id"]
            _, sess = c1.req("POST", "/v1/sessions", {
                "project_id": proj_id,
                "posture": "build",
                "provider_id": "ollama",
                "model": "qual-deterministic",
            })
            sess_id = sess["id"]

            op_id = str(uuid.uuid4())
            prep_deadline = time.time() + 45.0
            while True:
                try:
                    c1.req("POST", f"/v1/sessions/{sess_id}/prompts", {
                        "operation_id": op_id,
                        "text": "Run py09_witness.py with the python tool to create py09-den-witness.txt and retained stdout.",
                    })
                    break
                except RuntimeError as exc:
                    if "session_preparing" in str(exc) and time.time() < prep_deadline:
                        time.sleep(0.5)
                        continue
                    raise

            t0 = time.time()
            seen_started = False
            deadline = time.time() + 120.0
            while time.time() < deadline:
                time.sleep(1.0)
                _, detail = c1.req("GET", f"/v1/sessions/{sess_id}")
                status = str((detail or {}).get("status") or (detail or {}).get("state") or (detail or {}).get("run_state") or "").lower()
                is_running = (detail or {}).get("is_running")
                if status in ("running", "busy", "streaming", "in_progress") or is_running is True:
                    seen_started = True
                elif (seen_started or (time.time() - t0 > 4.0)) and (
                    is_running is False or status in ("idle", "completed", "ready", "stopped", "error", "failed")
                ):
                    break
        finally:
            stop_proc(pw1, pw1_fh)
            prov_srv.shutdown()

        # Verify settled file & parse Run #1 ToolResponse JSON
        witness_file = ws / "py09-den-witness.txt"
        if not witness_file.is_file():
            raise AssertionError("py09-den-witness.txt was not settled to workspace in Run #1")
        shutil.copy2(witness_file, out_dir / "py09-den-witness.txt")

        tool_text = (out_dir / "run1-tool-result.txt").read_text(encoding="utf-8")
        tool_json = None
        for line in tool_text.splitlines():
            s = line.strip()
            if s.startswith("{") and '"execution_id"' in s:
                tool_json = json.loads(s)
                break
        if not isinstance(tool_json, dict) or tool_json.get("backend") != "native-aot" or not tool_json.get("clean_completion"):
            raise AssertionError(f"Run #1 tool response invalid: {tool_text[:500]}")

        # Copy persisted receipts + raw retained stdout blob from cfg_dir
        receipts_dir = out_dir / "receipts"
        receipts_dir.mkdir(parents=True, exist_ok=True)
        for fname in ("executions.jsonl", "admissions.jsonl", "publications.jsonl"):
            for p in cfg_dir.rglob(fname):
                shutil.copy2(p, receipts_dir / fname)

        retained_out_dir = out_dir / "retained-output"
        retained_out_dir.mkdir(parents=True, exist_ok=True)
        retained_blobs = []
        for p in sorted(cfg_dir.rglob("*")):
            if p.is_file() and p.stat().st_size > 65000:
                try:
                    txt = p.read_text(encoding="utf-8", errors="ignore")
                except Exception:
                    continue
                if "PY09_RETAINED_STDOUT_BEGIN" in txt and "PY09_RETAINED_STDOUT_END" in txt:
                    dest = retained_out_dir / f"retained-stdout-{p.name}.txt"
                    shutil.copy2(p, dest)
                    retained_blobs.append({
                        "source_rel": str(p.relative_to(cfg_dir)),
                        "archived_file": str(dest.relative_to(out_dir)),
                        "bytes": p.stat().st_size,
                        "sha256": sha256_file(dest),
                    })
        if not retained_blobs:
            raise AssertionError(f"no retained stdout blob (>65KB with PY09_RETAINED_STDOUT_BEGIN/END) found in {cfg_dir}")

        # --- Phase 2: Restart (reopen) pw serve against SAME cfg_dir & workspace ---
        pw2_port = free_port()
        pw2, pw2_fh = start_pw(
            binary, engine_root, repo_root, scratch, cfg_dir,
            pw2_port, token, out_dir / "run2-reopened-pw-server.log", out_dir / "run2-llm-requests.jsonl",
        )
        vite_proc = None
        vite_fh = None
        try:
            base2 = f"http://127.0.0.1:{pw2_port}"
            wait_for_http(base2 + "/health", timeout_s=60.0)
            c2 = PwClient(base2, token)

            # Capture API-scope reopened endpoints (labeled explicitly as API scope)
            _, api_sess = c2.req("GET", f"/v1/sessions/{sess_id}")
            _, api_msgs = c2.req("GET", f"/v1/sessions/{sess_id}/messages")
            api_export = c2.req_text("GET", f"/v1/sessions/{sess_id}/export")
            (out_dir / "api-reopen-session.json").write_text(json.dumps(api_sess, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            (out_dir / "api-reopen-messages.json").write_text(json.dumps(api_msgs, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            (out_dir / "api-reopen-export.md").write_text(api_export, encoding="utf-8")

            # --- Phase 3: Launch real lycaon-den Vite UI proxied to reopened pw Run #2 ---
            den_dir = repo_root / "lycaon-den"
            ui_port = free_port()
            vite_env = os.environ.copy()
            vite_env.pop("PW_PORTABLE_PYTHON_IMAGE_DIR", None)
            vite_env.pop("PW_PORTABLE_PYTHON_ENGINE_DIR", None)
            vite_env["VITE_LYCAON_PROXY"] = "1"
            vite_env["VITE_LYCAON_PROXY_TARGET"] = base2
            vite_env["VITE_LYCAON_API_TOKEN"] = token
            vite_env["LYCAON_E2E_FROZEN"] = "1"
            vite_fh = (out_dir / "lycaon-den-vite.log").open("w", encoding="utf-8")
            vite_proc = subprocess.Popen(
                ["npx", "vite", "--port", str(ui_port), "--host", "127.0.0.1", "--strictPort"],
                cwd=str(den_dir),
                env=vite_env,
                stdout=vite_fh,
                stderr=subprocess.STDOUT,
            )
            ui_url = f"http://127.0.0.1:{ui_port}"
            wait_for_http(ui_url, timeout_s=60.0)

            # Drive real lycaon-den Files & Den UI via Playwright Chromium
            ui_cmd = [
                "node",
                str(script_dir / "verify-py09-den-ui.mjs"),
                "--ui-url", ui_url,
                "--project-id", proj_id,
                "--session-id", sess_id,
                "--execution-id", str(tool_json["execution_id"]),
                "--out-dir", str(out_dir),
            ]
            ui_res = subprocess.run(ui_cmd, cwd=str(den_dir), capture_output=True, text=True, timeout=180)
            (out_dir / "ui-reopen-driver.log").write_text(
                f"exit_code={ui_res.returncode}\n=== STDOUT ===\n{ui_res.stdout}\n=== STDERR ===\n{ui_res.stderr}\n",
                encoding="utf-8",
            )
            if ui_res.returncode != 0:
                raise RuntimeError(f"Playwright UI reopen check failed (rc={ui_res.returncode}):\n{ui_res.stdout}\n{ui_res.stderr}")
        finally:
            if vite_proc is not None:
                stop_proc(vite_proc, vite_fh)
            stop_proc(pw2, pw2_fh)

        ui_verdict = json.loads((out_dir / "ui-reopen-verdict.json").read_text(encoding="utf-8"))
        summary = {
            "status": "PASS",
            "platform": f"{platform.system().lower()}-{platform.machine().lower()}",
            "binary_path": str(binary),
            "binary_sha256": sha256_file(binary),
            "pw_portable_python_image_dir": "UNSET",
            "pw_portable_python_engine_dir": "UNSET",
            "project_id": proj_id,
            "session_id": sess_id,
            "execution_id": tool_json["execution_id"],
            "backend": tool_json["backend"],
            "engine_tag": tool_json.get("engine_tag"),
            "stdout_truncated": tool_json.get("stdout_truncated"),
            "witness_file_sha256": sha256_file(out_dir / "py09-den-witness.txt"),
            "retained_output_blobs": retained_blobs,
            "api_scope_artifacts": [
                "api-reopen-session.json",
                "api-reopen-messages.json",
                "api-reopen-export.md",
            ],
            "desktop_ui_scope": ui_verdict,
        }
        (out_dir / "reopen-witness-summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"PASS [PY09 reopen witness]: {out_dir / 'reopen-witness-summary.json'}")
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    main()
