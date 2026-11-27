#!/usr/bin/env python3
"""PY09 Targeted Native macOS Tauri Desktop Shell Reopen Witness.

Executes on macOS arm64 with PW_PORTABLE_PYTHON_IMAGE_DIR and
PW_PORTABLE_PYTHON_ENGINE_DIR UNSET:
  1. Runs ONE real `native-aot` Python activity (`py09_witness.py`) through the
     staged `pw` sidecar (Run #1) that writes `py09-den-witness.txt` and emits
     >70 KiB stdout (retaining overflow stdout on disk).
  2. Stops `pw` Run #1 and restarts `pw` Run #2 against the SAME `LYCAON_CONFIG_DIR`,
     `LYCAON_ENGINE_ROOT`, and workspace.
  3. Launches the ACTUAL compiled native macOS Tauri desktop shell binary
     (`lycaon-den/src-tauri/target/debug/painted-wolf-code`) connected to the
     restarted `pw` Run #2 (NO Chromium / NO Playwright!).
  4. Captures native Tauri process & WKWebView tree, CoreGraphics window metadata
     (`CGWindowListCopyWindowInfo`), `screencapture` (or exact macOS TCC/display
     status), reopened `pw` Run #2 live requests from the native WKWebView,
     paired receipts (`executions.jsonl`, `admissions.jsonl`, `publications.jsonl`),
     settled `py09-den-witness.txt`, and raw retained stdout.
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
    print(f"PY09_LINE_{i:04d}: retained output witness payload for native Tauri reopen ({marker})")
print("PY09_RETAINED_STDOUT_END", marker)
"""

SWIFT_WINDOW_PROBE = r"""
import CoreGraphics
import Foundation

let targetPid = Int32(CommandLine.arguments[1]) ?? -1
let opts: CGWindowListOption = [.optionAll]
guard let list = CGWindowListCopyWindowInfo(opts, kCGNullWindowID) as? [[String: Any]] else {
    print("[]")
    exit(0)
}
var matched: [[String: Any]] = []
for w in list {
    let pid = (w[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value ?? -1
    let owner = (w[kCGWindowOwnerName as String] as? String) ?? ""
    if pid == targetPid || owner.lowercased().contains("painted") || owner.lowercased().contains("lycaon") {
        var entry: [String: Any] = [
            "window_number": (w[kCGWindowNumber as String] as? NSNumber)?.intValue ?? 0,
            "owner_pid": Int(pid),
            "owner_name": owner,
            "name": (w[kCGWindowName as String] as? String) ?? "",
            "layer": (w[kCGWindowLayer as String] as? NSNumber)?.intValue ?? 0,
            "is_onscreen": (w[kCGWindowIsOnscreen as String] as? NSNumber)?.boolValue ?? false,
        ]
        if let b = w[kCGWindowBounds as String] as? [String: Any] {
            entry["bounds"] = b
        }
        matched.append(entry)
    }
}
let data = try JSONSerialization.data(withJSONObject: matched, options: [.prettyPrinted, .sortedKeys])
print(String(data: data, encoding: .utf8) ?? "[]")
"""

WKWEBVIEW_DRIVER_JS = r"""
// Dev-only PY09 qualification driver injected ONLY via disposable Vite config
// into the genuine macOS Tauri WKWebView (zero production source edits).
(async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const startedAt = new Date().toISOString();
  const report = async (obj) => {
    try {
      await fetch("/__py09_report", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(obj, null, 2),
      });
    } catch (_) {}
  };

  const findByExactText = (text) => {
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT);
    let node;
    while ((node = walker.nextNode())) {
      const el = /** @type {HTMLElement} */ (node);
      if (el.offsetParent === null) continue;
      if ((el.innerText || "").trim() === text) return el;
    }
    return null;
  };

  const findClickableMatching = (re) => {
    const els = Array.from(document.querySelectorAll("button, a, [role='button'], [role='treeitem'], [role='radio'], label, li"));
    for (const el of els) {
      if (el.offsetParent === null) continue;
      const t = (el.innerText || "").trim();
      if (re.test(t)) return el;
    }
    return null;
  };

  try {
    // 1. Wait for SolidJS UI mount
    for (let i = 0; i < 60; i += 1) {
      if ((document.body?.innerText || "").trim().length > 20) break;
      await sleep(500);
    }

    // 2. Step past welcome wizard if shown, selecting "Files and chat", and wait for project "py09-native-tauri-reopen"
    let projEl = null;
    for (let i = 0; i < 45; i += 1) {
      projEl = findByExactText("py09-native-tauri-reopen");
      if (projEl) break;
      const bodyTxt = document.body?.innerText || "";
      if (bodyTxt.includes("How do you want to work?")) {
        const fc = findClickableMatching(/Files and chat/i);
        if (fc) { fc.click(); await sleep(300); }
        const cont = findClickableMatching(/^Continue$/i);
        if (cont) { cont.click(); await sleep(1000); continue; }
      } else if (bodyTxt.includes("2 / 2") || bodyTxt.includes("1 / 2") || bodyTxt.includes("Local model")) {
        const skip = findClickableMatching(/^(Skip|Skip for now|Not now|Continue|Done|Finish|Get started|Start working|Open workspace)$/i);
        if (skip) { skip.click(); await sleep(1000); continue; }
      }
      await sleep(800);
    }

    // 3. Click project "py09-native-tauri-reopen" if not already inside its Chats view
    if (projEl && !(document.body?.innerText || "").includes("All chats")) {
      projEl.click();
      await sleep(2500);
    }

    // 4. Click the reopened "Finished" chat in the Chats list
    for (let i = 0; i < 15; i += 1) {
      const fin = findClickableMatching(/Finished/i);
      if (fin) {
        fin.click();
        await sleep(2500);
        break;
      }
      await sleep(500);
    }

    // 5. Wait for reopened Python activity in Den DOM
    let denDom = "";
    for (let i = 0; i < 20; i += 1) {
      denDom = document.body?.innerText || "";
      if (
        denDom.includes("PY09 native desktop witness completed") ||
        denDom.includes("py09_witness.py") ||
        denDom.includes("PY09_RETAINED_STDOUT_BEGIN")
      ) {
        break;
      }
      await sleep(600);
    }
    const denHasSession =
      denDom.includes("PY09 native desktop witness completed") ||
      denDom.includes("py09_witness.py") ||
      denDom.includes("PY09_RETAINED_STDOUT_BEGIN");

    // 6. Switch to Files surface via data-testid="project-files-entry" and click py09-den-witness.txt
    const filesEntry = document.querySelector('[data-testid="project-files-entry"]');
    if (filesEntry) {
      filesEntry.click();
      await sleep(2000);
    } else {
      const fBtn = findClickableMatching(/^Files$/i);
      if (fBtn) { fBtn.click(); await sleep(2000); }
    }

    let witnessEl = null;
    for (let i = 0; i < 20; i += 1) {
      witnessEl =
        document.querySelector('[data-testid="files-tree-file"][data-path="py09-den-witness.txt"]') ||
        findByExactText("py09-den-witness.txt");
      if (witnessEl) {
        witnessEl.click();
        await sleep(2500);
        break;
      }
      await sleep(500);
    }

    let filesDom = "";
    for (let i = 0; i < 15; i += 1) {
      filesDom = document.body?.innerText || "";
      if (filesDom.includes("PY09_DEN_WITNESS_OK_NATIVE_AOT")) break;
      await sleep(500);
    }

    const isTauri =
      typeof window.__TAURI_INTERNALS__ !== "undefined" ||
      typeof window.__TAURI__ !== "undefined";

    await report({
      status: denHasSession && filesDom.includes("py09-den-witness.txt") && filesDom.includes("PY09_DEN_WITNESS_OK_NATIVE_AOT") ? "PASS" : "FAIL",
      started_at: startedAt,
      completed_at: new Date().toISOString(),
      url: window.location.href,
      user_agent: navigator.userAgent,
      is_tauri_wkwebview: isTauri,
      has_tauri_internals: typeof window.__TAURI_INTERNALS__ !== "undefined",
      project_name: "py09-native-tauri-reopen",
      den_reopened_has_session: denHasSession,
      den_dom_excerpt: denDom.slice(0, 2500),
      files_reopened_has_artifact_name: filesDom.includes("py09-den-witness.txt"),
      files_reopened_has_marker: filesDom.includes("PY09_DEN_WITNESS_OK_NATIVE_AOT"),
      files_dom_excerpt: filesDom.slice(0, 2500),
    });
  } catch (err) {
    await report({
      status: "ERROR",
      error: String(err),
      url: window.location.href,
      user_agent: navigator.userAgent,
      is_tauri_wkwebview: typeof window.__TAURI_INTERNALS__ !== "undefined",
      dom_head: (document.body?.innerText || "").slice(0, 1500),
    });
  }
})();
"""


def write_disposable_vite_config(den_dir: Path, scratch: Path, obs_out_path: Path) -> Path:
    """Create a disposable Vite config in scratch that wraps lycaon-den/vite.config.ts
    with a dev-only plugin serving /__py09_driver.js and /__py09_report."""
    driver_js_path = scratch / "py09-wkwebview-driver.js"
    driver_js_path.write_text(WKWEBVIEW_DRIVER_JS, encoding="utf-8")
    cfg_path = scratch / "vite.py09-driver.config.ts"
    base_cfg = (den_dir / "vite.config.ts").as_posix()
    cfg_src = f"""import fs from "node:fs";
import baseConfigFn from {json.dumps(base_cfg)};

const DRIVER_JS_PATH = {json.dumps(str(driver_js_path))};
const OBS_OUT_PATH = {json.dumps(str(obs_out_path))};

export default (env: any) => {{
  const cfg: any = typeof baseConfigFn === "function" ? baseConfigFn(env) : baseConfigFn;
  cfg.plugins = [
    ...(cfg.plugins || []),
    {{
      name: "py09-dev-wkwebview-driver",
      transformIndexHtml(html: string) {{
        return html.replace(
          "</body>",
          `<script type="module" src="/__py09_driver.js"></script></body>`,
        );
      }},
      configureServer(server: any) {{
        server.middlewares.use("/__py09_driver.js", (_req: any, res: any) => {{
          res.setHeader("Content-Type", "application/javascript; charset=utf-8");
          res.end(fs.readFileSync(DRIVER_JS_PATH, "utf-8"));
        }});
        server.middlewares.use("/__py09_report", (req: any, res: any) => {{
          let body = "";
          req.on("data", (chunk: any) => {{ body += chunk.toString(); }});
          req.on("end", () => {{
            fs.writeFileSync(OBS_OUT_PATH, body, "utf-8");
            res.statusCode = 200;
            res.setHeader("Content-Type", "application/json");
            res.end(JSON.stringify({{ ok: true }}));
          }});
        }});
      }},
    }},
  ];
  return cfg;
}};
"""
    cfg_path.write_text(cfg_src, encoding="utf-8")
    return cfg_path


def sha256_file(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def start_deterministic_provider(req_log_path: Path, tool_result_path: Path):
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
                                "content": "PY09 native desktop witness completed: py09-den-witness.txt written and retained stdout captured.",
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
    return srv, port


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
    if proc is not None and proc.poll() is None:
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
    ap.add_argument("--sidecar", required=True, help="Path to staged pw sidecar binary")
    ap.add_argument("--tauri-bin", required=True, help="Path to compiled native Tauri desktop binary")
    ap.add_argument("--engine-root", required=True)
    ap.add_argument("--repo-root", required=True)
    ap.add_argument("--out-dir", required=True)
    args = ap.parse_args()

    sidecar = Path(args.sidecar).resolve()
    tauri_bin = Path(args.tauri_bin).resolve()
    engine_root = Path(args.engine_root).resolve()
    repo_root = Path(args.repo_root).resolve()
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)

    scratch = Path(tempfile.mkdtemp(prefix="py09-native-tauri-")).resolve()
    try:
        ws = scratch / "workspace"
        ws.mkdir(parents=True, exist_ok=True)
        (ws / "py09_witness.py").write_text(WITNESS_SCRIPT, encoding="utf-8")
        cfg_dir = scratch / "cfg"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        token = uuid.uuid4().hex + uuid.uuid4().hex
        (cfg_dir / "api.token").write_text(token + "\n", encoding="utf-8")

        # --- Phase 1: Run #1 pw serve & native-aot Python activity ---
        prov_srv, prov_port = start_deterministic_provider(
            out_dir / "run1-provider-requests.jsonl",
            out_dir / "run1-tool-result.txt",
        )
        pw1_port = free_port()
        pw1, pw1_fh = start_pw(
            sidecar, engine_root, repo_root, scratch, cfg_dir,
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
                "name": "py09-native-tauri-reopen",
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

        # Verify settled file, tool response, receipts, retained stdout
        witness_file = ws / "py09-den-witness.txt"
        if not witness_file.is_file():
            raise AssertionError("py09-den-witness.txt was not settled in Run #1")
        shutil.copy2(witness_file, out_dir / "py09-den-witness.txt")

        tool_text = (out_dir / "run1-tool-result.txt").read_text(encoding="utf-8")
        tool_json = next(
            json.loads(line.strip())
            for line in tool_text.splitlines()
            if line.strip().startswith("{") and '"execution_id"' in line
        )
        if tool_json.get("backend") != "native-aot" or not tool_json.get("clean_completion"):
            raise AssertionError(f"Run #1 tool response invalid: {tool_json}")

        receipts_dir = out_dir / "receipts"
        receipts_dir.mkdir(parents=True, exist_ok=True)
        for fname in ("executions.jsonl", "admissions.jsonl", "publications.jsonl"):
            for p in cfg_dir.rglob(fname):
                shutil.copy2(p, receipts_dir / fname)

        retained_dir = out_dir / "retained-output"
        retained_dir.mkdir(parents=True, exist_ok=True)
        retained_blobs = []
        for p in sorted(cfg_dir.rglob("*")):
            if p.is_file() and p.stat().st_size > 65000:
                txt = p.read_text(encoding="utf-8", errors="ignore")
                if "PY09_RETAINED_STDOUT_BEGIN" in txt and "PY09_RETAINED_STDOUT_END" in txt:
                    dest = retained_dir / f"retained-stdout-{p.name}.txt"
                    shutil.copy2(p, dest)
                    retained_blobs.append({
                        "source_rel": str(p.relative_to(cfg_dir)),
                        "archived_file": str(dest.relative_to(out_dir)),
                        "bytes": p.stat().st_size,
                        "sha256": sha256_file(dest),
                    })
        if not retained_blobs:
            raise AssertionError("no retained stdout blob found in cfg_dir")

        # --- Phase 2: Restart pw Run #2 + launch REAL native macOS Tauri desktop binary ---
        pw2_port = free_port()
        pw2, pw2_fh = start_pw(
            sidecar, engine_root, repo_root, scratch, cfg_dir,
            pw2_port, token, out_dir / "run2-reopened-pw-server.log", out_dir / "run2-llm-requests.jsonl",
        )
        vite_proc = None
        vite_fh = None
        tauri_proc = None
        tauri_fh = None
        native_windows = []
        screencapture_result = {"attempted": True, "succeeded": False, "detail": ""}
        try:
            base2 = f"http://127.0.0.1:{pw2_port}"
            wait_for_http(base2 + "/health", timeout_s=60.0)
            c2 = PwClient(base2, token)
            _, api_sess = c2.req("GET", f"/v1/sessions/{sess_id}")
            _, api_msgs = c2.req("GET", f"/v1/sessions/{sess_id}/messages")
            api_export = c2.req_text("GET", f"/v1/sessions/{sess_id}/export")
            (out_dir / "api-reopen-session.json").write_text(json.dumps(api_sess, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            (out_dir / "api-reopen-messages.json").write_text(json.dumps(api_msgs, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            (out_dir / "api-reopen-export.md").write_text(api_export, encoding="utf-8")

            # Start Vite on port 1420 with disposable dev-only WKWebView driver config (zero repo edits)
            den_dir = repo_root / "lycaon-den"
            obs_path = out_dir / "wkwebview-dom-observation.json"
            vite_cfg_path = write_disposable_vite_config(den_dir, scratch, obs_path)
            vite_env = os.environ.copy()
            vite_env.pop("PW_PORTABLE_PYTHON_IMAGE_DIR", None)
            vite_env.pop("PW_PORTABLE_PYTHON_ENGINE_DIR", None)
            vite_env["VITE_LYCAON_PROXY"] = "1"
            vite_env["VITE_LYCAON_PROXY_TARGET"] = base2
            vite_env["VITE_LYCAON_API_URL"] = base2
            vite_env["VITE_LYCAON_API_TOKEN"] = token
            vite_env["LYCAON_E2E_FROZEN"] = "1"
            vite_fh = (out_dir / "lycaon-den-vite.log").open("w", encoding="utf-8")
            vite_proc = subprocess.Popen(
                ["npx", "vite", "--config", str(vite_cfg_path), "--port", "1420", "--host", "127.0.0.1", "--strictPort"],
                cwd=str(den_dir),
                env=vite_env,
                stdout=vite_fh,
                stderr=subprocess.STDOUT,
            )
            wait_for_http("http://127.0.0.1:1420", timeout_s=60.0)

            # Launch the ACTUAL native macOS Tauri desktop binary (painted-wolf-code)
            tauri_env = os.environ.copy()
            tauri_env.pop("PW_PORTABLE_PYTHON_IMAGE_DIR", None)
            tauri_env.pop("PW_PORTABLE_PYTHON_ENGINE_DIR", None)
            tauri_env["LYCAON_ATTACH_ONLY"] = "1"
            tauri_env["LYCAON_DEV"] = "1"
            tauri_env["LYCAON_CONFIG_DIR"] = str(cfg_dir)
            tauri_env["LYCAON_ENGINE_ROOT"] = str(engine_root)
            tauri_env["VITE_LYCAON_API_URL"] = base2
            tauri_env["VITE_LYCAON_API_TOKEN"] = token
            tauri_env["NO_PROXY"] = "127.0.0.1,localhost"
            tauri_fh = (out_dir / "native-tauri-shell.log").open("w", encoding="utf-8")
            tauri_proc = subprocess.Popen(
                [str(tauri_bin)],
                cwd=str(den_dir / "src-tauri"),
                env=tauri_env,
                stdout=tauri_fh,
                stderr=subprocess.STDOUT,
            )

            # Wait up to 75s for the in-WKWebView dev driver to complete Den + Files UI assertions & report
            obs_deadline = time.time() + 75.0
            while time.time() < obs_deadline:
                if obs_path.is_file() and obs_path.stat().st_size > 10:
                    break
                time.sleep(1.0)

            # Capture live process tree proving native Tauri + WKWebView (zero Chromium)
            ps_out = subprocess.run(["ps", "-ef"], capture_output=True, text=True).stdout
            relevant_ps = [
                line for line in ps_out.splitlines()
                if any(k in line for k in ("painted-wolf-code", "WebKit", str(tauri_proc.pid), "pw "))
            ]
            (out_dir / "native-tauri-processes.txt").write_text("\n".join(relevant_ps) + "\n", encoding="utf-8")

            # Query CoreGraphics window list for the live Tauri PID
            swift_probe = scratch / "probe_windows.swift"
            swift_probe.write_text(SWIFT_WINDOW_PROBE, encoding="utf-8")
            win_res = subprocess.run(["swift", str(swift_probe), str(tauri_proc.pid)], capture_output=True, text=True, timeout=60)
            (out_dir / "native-tauri-windows.json").write_text(win_res.stdout or "[]\n", encoding="utf-8")
            try:
                native_windows = json.loads(win_res.stdout or "[]")
            except Exception:
                native_windows = []

            # Pick the main onscreen Tauri window (e.g. 1280x800, is_onscreen=True) for screencapture.
            win_id = None
            for w in native_windows:
                b = w.get("bounds") or {}
                if w.get("owner_pid") == tauri_proc.pid and w.get("is_onscreen") and b.get("Height", 0) >= 200:
                    win_id = w.get("window_number")
                    break
            if win_id is None:
                for w in native_windows:
                    if w.get("owner_pid") == tauri_proc.pid and w.get("layer", 0) == 0 and w.get("window_number"):
                        win_id = w["window_number"]
                        break
            cap_path = out_dir / "native-tauri-window.png"
            if win_id is not None:
                sc = subprocess.run(["screencapture", "-l", str(win_id), "-x", str(cap_path)], capture_output=True, text=True)
                if sc.returncode == 0 and cap_path.is_file() and cap_path.stat().st_size > 0:
                    screencapture_result = {"attempted": True, "succeeded": True, "window_id": win_id, "file": cap_path.name, "bytes": cap_path.stat().st_size}
                else:
                    screencapture_result = {"attempted": True, "succeeded": False, "window_id": win_id, "detail": (sc.stderr or sc.stdout or f"exit={sc.returncode}").strip()}
            else:
                sc = subprocess.run(["screencapture", "-x", str(cap_path)], capture_output=True, text=True)
                if sc.returncode == 0 and cap_path.is_file() and cap_path.stat().st_size > 0:
                    screencapture_result = {"attempted": True, "succeeded": True, "file": cap_path.name, "bytes": cap_path.stat().st_size}
                else:
                    screencapture_result = {"attempted": True, "succeeded": False, "detail": (sc.stderr or sc.stdout or f"exit={sc.returncode}").strip()}
        finally:
            stop_proc(tauri_proc, tauri_fh)
            stop_proc(vite_proc, vite_fh)
            stop_proc(pw2, pw2_fh)

        # Load in-WKWebView DOM observation reported by the dev-only driver
        wkwebview_obs = None
        obs_path = out_dir / "wkwebview-dom-observation.json"
        if obs_path.is_file():
            try:
                wkwebview_obs = json.loads(obs_path.read_text(encoding="utf-8"))
            except Exception as exc:
                wkwebview_obs = {"parse_error": str(exc)}

        wk_ok = (
            isinstance(wkwebview_obs, dict)
            and wkwebview_obs.get("status") == "PASS"
            and wkwebview_obs.get("is_tauri_wkwebview") is True
            and wkwebview_obs.get("den_reopened_has_session") is True
            and wkwebview_obs.get("files_reopened_has_artifact_name") is True
            and wkwebview_obs.get("files_reopened_has_marker") is True
        )

        # Inspect Run #2 pw-server log for live requests from the native Tauri WKWebView
        pw2_log = (out_dir / "run2-reopened-pw-server.log").read_text(encoding="utf-8", errors="ignore")
        webview_hit_projects = "/v1/projects" in pw2_log
        webview_hit_host = "/v1/host" in pw2_log
        webview_hit_events = "/v1/events" in pw2_log
        file_info = subprocess.run(["file", str(tauri_bin)], capture_output=True, text=True).stdout.strip()

        summary = {
            "status": "PASS" if (native_windows and webview_hit_projects and wk_ok) else "PARTIAL",
            "platform": f"{platform.system().lower()}-{platform.machine().lower()}",
            "pw_portable_python_image_dir": "UNSET",
            "pw_portable_python_engine_dir": "UNSET",
            "native_tauri_binary": {
                "path": str(tauri_bin),
                "sha256": sha256_file(tauri_bin),
                "file_info": file_info,
            },
            "pw_sidecar_binary": {
                "path": str(sidecar),
                "sha256": sha256_file(sidecar),
            },
            "project_id": proj_id,
            "session_id": sess_id,
            "execution_id": tool_json["execution_id"],
            "backend": tool_json["backend"],
            "engine_tag": tool_json.get("engine_tag"),
            "stdout_truncated": tool_json.get("stdout_truncated"),
            "witness_file_sha256": sha256_file(out_dir / "py09-den-witness.txt"),
            "retained_output_blobs": retained_blobs,
            "native_tauri_window_scope": {
                "windows_observed": native_windows,
                "webview_requests_observed_in_run2_pw_log": {
                    "v1_host": webview_hit_host,
                    "v1_projects": webview_hit_projects,
                    "v1_events": webview_hit_events,
                },
                "wkwebview_dom_observation_file": "wkwebview-dom-observation.json",
                "wkwebview_dom_observation_summary": {
                    "status": (wkwebview_obs or {}).get("status"),
                    "is_tauri_wkwebview": (wkwebview_obs or {}).get("is_tauri_wkwebview"),
                    "has_tauri_internals": (wkwebview_obs or {}).get("has_tauri_internals"),
                    "user_agent": (wkwebview_obs or {}).get("user_agent"),
                    "den_reopened_has_session": (wkwebview_obs or {}).get("den_reopened_has_session"),
                    "files_reopened_has_artifact_name": (wkwebview_obs or {}).get("files_reopened_has_artifact_name"),
                    "files_reopened_has_marker": (wkwebview_obs or {}).get("files_reopened_has_marker"),
                },
                "screencapture": screencapture_result,
                "process_snapshot": "native-tauri-processes.txt",
                "window_metadata_file": "native-tauri-windows.json",
            },
            "api_scope_artifacts": [
                "api-reopen-session.json",
                "api-reopen-messages.json",
                "api-reopen-export.md",
            ],
            "receipt_artifacts": [
                "receipts/executions.jsonl",
                "receipts/admissions.jsonl",
                "receipts/publications.jsonl",
            ],
        }
        (out_dir / "native-reopen-summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"DONE [PY09 native macOS Tauri reopen]: status={summary['status']} windows={len(native_windows)} webview_projects={webview_hit_projects} wkwebview_dom_ok={wk_ok}")
        if not wk_ok:
            raise SystemExit(1)
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    main()
