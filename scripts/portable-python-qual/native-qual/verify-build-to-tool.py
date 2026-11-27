#!/usr/bin/env python3
"""Build-to-tool qualification driver for Portable Python.

Launches the REAL staged `pw` sidecar binary (`serve`) with
PW_PORTABLE_PYTHON_IMAGE_DIR and PW_PORTABLE_PYTHON_ENGINE_DIR UNSET,
invokes the registered `python` tool on the frozen workbook workload
(`config/repos.yaml` -> `repositories.xlsx`) via a local deterministic
loopback provider, captures raw requests/responses/receipts, and
independently verifies:
  1. Workbook readback (sheets, row count, freeze panes, split totals)
     using stdlib zipfile + ElementTree (independent of openpyxl).
  2. Native engine identity (native-aot, pw-cpython-wasi-engine-v0.1.0,
     c8dc215cbd465623592b996f56c394124053d3b2, ABI v1) and checked-hash
     .pyc image identity from staged manifests, tool response, and
     persisted executions.jsonl receipts.
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
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path

EXPECTED_ENGINE_TAG = "pw-cpython-wasi-engine-v0.1.0"
EXPECTED_SOURCE_COMMIT = "c8dc215cbd465623592b996f56c394124053d3b2"
EXPECTED_ABI_VERSION = 1


def sha256_file(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def start_deterministic_provider(req_log_path: Path, tool_result_path: Path):
    """Start a local loopback Ollama-compatible /api/chat + /api/show server."""
    state = {"calls": 0, "tool_results": []}
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
                                            "arguments": {"script": "workbook.py"},
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
                        state["tool_results"].append(content)
                        tool_result_path.write_text(content, encoding="utf-8")
                        chunk = {
                            "message": {
                                "role": "assistant",
                                "content": "Workbook generated and verified.",
                            },
                            "done": True,
                            "prompt_eval_count": 240,
                            "eval_count": 8,
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
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
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


def wait_for_health(base_url: str, timeout_s: float = 45.0) -> None:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(base_url + "/health", timeout=2.0) as resp:
                if resp.status == 200:
                    return
        except Exception:
            pass
        time.sleep(0.5)
    raise RuntimeError(f"pw sidecar did not become healthy at {base_url} within {timeout_s}s")


def verify_workbook_independent(xlsx_path: Path) -> dict:
    """Independently read and verify repositories.xlsx using stdlib zipfile + XML."""
    if not xlsx_path.exists():
        raise AssertionError(f"missing expected workbook: {xlsx_path}")

    ns = {
        "m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main",
        "r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
    }
    with zipfile.ZipFile(xlsx_path, "r") as zf:
        wb_xml = ET.fromstring(zf.read("xl/workbook.xml"))
        rels_xml = ET.fromstring(zf.read("xl/_rels/workbook.xml.rels"))
        rel_map = {
            rel.attrib["Id"]: rel.attrib["Target"].lstrip("/")
            for rel in rels_xml
            if "Id" in rel.attrib and "Target" in rel.attrib
        }

        shared_strings = []
        if "xl/sharedStrings.xml" in zf.namelist():
            ss_xml = ET.fromstring(zf.read("xl/sharedStrings.xml"))
            for si in ss_xml.findall("m:si", ns):
                parts = [t.text or "" for t in si.findall(".//m:t", ns)]
                shared_strings.append("".join(parts))

        sheets = []
        sheet_details = {}
        for sh in wb_xml.findall("m:sheets/m:sheet", ns):
            name = sh.attrib["name"]
            rid = sh.attrib["{http://schemas.openxmlformats.org/officeDocument/2006/relationships}id"]
            target = rel_map[rid]
            if not target.startswith("xl/"):
                target = "xl/" + target
            sheets.append(name)
            sh_xml = ET.fromstring(zf.read(target))

            pane = sh_xml.find(".//m:sheetViews/m:sheetView/m:pane", ns)
            freeze_cell = pane.attrib.get("topLeftCell") if pane is not None else None
            ysplit = pane.attrib.get("ySplit") if pane is not None else None

            rows = []
            for row_el in sh_xml.findall(".//m:sheetData/m:row", ns):
                vals = []
                for c in row_el.findall("m:c", ns):
                    t_attr = c.attrib.get("t", "")
                    if t_attr == "inlineStr":
                        val = "".join(t.text or "" for t in c.findall(".//m:t", ns))
                    else:
                        v_el = c.find("m:v", ns)
                        raw_v = v_el.text if v_el is not None else ""
                        if t_attr == "s" and raw_v != "":
                            val = shared_strings[int(raw_v)]
                        else:
                            val = raw_v
                    vals.append(val)
                rows.append(vals)

            sheet_details[name] = {
                "row_count": len(rows),
                "freeze_top_left": freeze_cell,
                "freeze_ysplit": ysplit,
                "header": rows[0] if rows else [],
                "rows": rows,
            }

    if sheets != ["repositories", "counts_by_split_language"]:
        raise AssertionError(f"unexpected sheet names: {sheets}")

    r1 = sheet_details["repositories"]
    if r1["row_count"] != 17:
        raise AssertionError(f"expected 17 rows in repositories sheet (header+16), got {r1['row_count']}")
    if r1["header"] != ["name", "language", "split", "license", "pinned_commit"]:
        raise AssertionError(f"unexpected repositories header: {r1['header']}")
    if r1["freeze_top_left"] != "A2" or r1["freeze_ysplit"] != "1":
        raise AssertionError(f"unexpected repositories freeze panes: {r1['freeze_top_left']} / {r1['freeze_ysplit']}")

    r2 = sheet_details["counts_by_split_language"]
    if r2["freeze_top_left"] != "A2" or r2["freeze_ysplit"] != "1":
        raise AssertionError(f"unexpected counts_by_split_language freeze panes: {r2['freeze_top_left']} / {r2['freeze_ysplit']}")
    split_totals = {row[0]: int(row[1]) for row in r2["rows"] if len(row) == 2 and row[0] in ("train", "holdout")}
    if split_totals != {"train": 13, "holdout": 3}:
        raise AssertionError(f"unexpected split totals in counts_by_split_language: {split_totals}")

    return {
        "xlsx_sha256": sha256_file(xlsx_path),
        "sheets": sheets,
        "repositories_rows": r1["row_count"] - 1,
        "repositories_freeze": r1["freeze_top_left"],
        "counts_freeze": r2["freeze_top_left"],
        "split_totals": split_totals,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--binary", required=True, help="Path to staged pw sidecar binary")
    ap.add_argument("--engine-root", required=True, help="Path to staged engine-root")
    ap.add_argument("--repo-root", required=True, help="Path to repository checkout")
    ap.add_argument("--out-dir", required=True, help="Output directory for raw evidence")
    ap.add_argument("--label", required=True, help="Run label (e.g. fresh-stage or upgraded-stage)")
    ap.add_argument("--expected-image-id", default="", help="Optional expected image_id to assert")
    args = ap.parse_args()

    binary = Path(args.binary).resolve()
    engine_root = Path(args.engine_root).resolve()
    repo_root = Path(args.repo_root).resolve()
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    label = args.label

    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise SystemExit(f"error: sidecar binary not executable: {binary}")

    # Verify staged engine & image manifests before launching.
    lib_name = "libpw_cpython_wasi.dylib" if platform.system() == "Darwin" else "libpw_cpython_wasi.so"
    engine_manifest_path = engine_root / "portable-python" / "engine" / "artifact-manifest.json"
    engine_lib_path = engine_root / "portable-python" / "engine" / lib_name
    image_manifest_path = engine_root / "portable-python" / "image" / "image-manifest.json"

    for req_path in (engine_manifest_path, engine_lib_path, image_manifest_path):
        if not req_path.is_file():
            raise SystemExit(f"error: missing staged artifact {req_path}")

    engine_manifest = json.loads(engine_manifest_path.read_text(encoding="utf-8"))
    image_manifest = json.loads(image_manifest_path.read_text(encoding="utf-8"))
    actual_lib_sha = sha256_file(engine_lib_path)

    if engine_manifest.get("engine_tag") != EXPECTED_ENGINE_TAG:
        raise SystemExit(f"error: engine_tag={engine_manifest.get('engine_tag')} want {EXPECTED_ENGINE_TAG}")
    if engine_manifest.get("source_commit") != EXPECTED_SOURCE_COMMIT:
        raise SystemExit(f"error: source_commit={engine_manifest.get('source_commit')} want {EXPECTED_SOURCE_COMMIT}")
    if engine_manifest.get("abi_version") != EXPECTED_ABI_VERSION:
        raise SystemExit(f"error: abi_version={engine_manifest.get('abi_version')} want {EXPECTED_ABI_VERSION}")
    if engine_manifest.get("library_sha256") != actual_lib_sha:
        raise SystemExit(f"error: library sha mismatch: manifest={engine_manifest.get('library_sha256')} actual={actual_lib_sha}")

    image_id = image_manifest.get("image_id", "")
    if args.expected_image_id and image_id != args.expected_image_id:
        raise SystemExit(f"error: image_id={image_id} want {args.expected_image_id}")

    # Prepare clean canonical workspace (resolved to avoid /var -> /private/var symlink on macOS).
    scratch = Path(tempfile.mkdtemp(prefix=f"pp-b2t-{label}-")).resolve()
    try:
        ws = scratch / "workspace"
        (ws / "config").mkdir(parents=True, exist_ok=True)
        fixtures_dir = repo_root / "scripts" / "portable-python-qual" / "bench" / "fixtures"
        shutil.copy2(fixtures_dir / "repos.yaml", ws / "config" / "repos.yaml")
        shutil.copy2(fixtures_dir / "workbook.py", ws / "workbook.py")

        cfg_dir = scratch / "cfg"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        llm_debug_file = out_dir / f"{label}-llm-requests.jsonl"
        prov_req_log = out_dir / f"{label}-provider-requests.jsonl"
        tool_result_file = out_dir / f"{label}-tool-result.txt"

        prov_srv, prov_port, prov_state = start_deterministic_provider(prov_req_log, tool_result_file)
        pw_port = free_port()
        token = uuid.uuid4().hex + uuid.uuid4().hex

        # Minimal clean env with PW_PORTABLE_PYTHON_IMAGE_DIR and PW_PORTABLE_PYTHON_ENGINE_DIR UNSET!
        env = {
            "HOME": str(scratch),
            "PATH": "/usr/bin:/bin",
            "LYCAON_ADDR": f"127.0.0.1:{pw_port}",
            "LYCAON_CONFIG_DIR": str(cfg_dir),
            "LYCAON_ENGINE_ROOT": str(engine_root),
            "LYCAON_API_TOKEN": token,
            "LYCAON_DEV": "1",
            "LYCAON_LLM_DEBUG": "1",
            "LYCAON_LLM_DEBUG_FILE": str(llm_debug_file),
            "NO_PROXY": "127.0.0.1,localhost",
        }
        assert "PW_PORTABLE_PYTHON_IMAGE_DIR" not in env
        assert "PW_PORTABLE_PYTHON_ENGINE_DIR" not in env

        srv_log_path = out_dir / f"{label}-pw-server.log"
        srv_log = srv_log_path.open("w", encoding="utf-8")
        proc = subprocess.Popen(
            [str(binary), "serve"],
            env=env,
            stdout=srv_log,
            stderr=subprocess.STDOUT,
        )
        t0 = time.time()
        try:
            base_url = f"http://127.0.0.1:{pw_port}"
            wait_for_health(base_url, timeout_s=60.0)
            client = PwClient(base_url, token)

            client.req("POST", "/v1/providers", {
                "id": "ollama",
                "kind": "ollama",
                "base_url": f"http://127.0.0.1:{prov_port}/v1",
                "requires_api_key": False,
                "models": [{"id": "qual-deterministic"}],
            })
            _, proj = client.req("POST", "/v1/projects", {
                "name": f"b2t-{label}",
                "roots": [{"path": str(ws), "is_primary": True}],
            })
            _, sess = client.req("POST", "/v1/sessions", {
                "project_id": proj["id"],
                "posture": "build",
                "provider_id": "ollama",
                "model": "qual-deterministic",
            })
            sess_id = sess["id"]

            op_id = str(uuid.uuid4())
            prep_deadline = time.time() + 45.0
            while True:
                try:
                    client.req("POST", f"/v1/sessions/{sess_id}/prompts", {
                        "operation_id": op_id,
                        "text": "Run workbook.py using the python tool to generate repositories.xlsx.",
                    })
                    break
                except RuntimeError as exc:
                    if "session_preparing" in str(exc) and time.time() < prep_deadline:
                        time.sleep(0.5)
                        continue
                    raise

            seen_started = False
            approved_ids = set()
            deadline = time.time() + 180.0
            last_detail = None
            while time.time() < deadline:
                time.sleep(1.0)
                try:
                    _, detail = client.req("GET", f"/v1/sessions/{sess_id}")
                    last_detail = detail
                except Exception:
                    continue

                try:
                    _, boot = client.req("GET", f"/v1/sessions/{sess_id}/bootstrap")
                except Exception:
                    boot = {}

                pending = []
                for container in (boot, detail):
                    if isinstance(container, dict):
                        for k in ("pending_checkpoints", "checkpoints"):
                            if isinstance(container.get(k), list):
                                pending.extend(container[k])
                        for k in ("checkpoint", "active_checkpoint"):
                            if isinstance(container.get(k), dict):
                                pending.append(container[k])
                for ck in pending:
                    cid = ck.get("id")
                    st = ck.get("status", "pending")
                    if cid and st in ("pending", "open", "waiting", "") and cid not in approved_ids:
                        payload = {"kind": ck.get("kind", "tool_approval"), "action": "approve"}
                        opts = (ck.get("plan") or {}).get("options") or ck.get("options") or []
                        for opt in opts:
                            if isinstance(opt, dict) and opt.get("action") == "approve" and opt.get("id"):
                                payload["option_id"] = opt["id"]
                                break
                        try:
                            client.req("POST", f"/v1/sessions/{sess_id}/checkpoints/{cid}", payload)
                            approved_ids.add(cid)
                        except Exception:
                            pass

                status = str((detail or {}).get("status") or (detail or {}).get("state") or (detail or {}).get("run_state") or "").lower()
                is_running = (detail or {}).get("is_running")
                if status in ("running", "busy", "streaming", "in_progress") or is_running is True:
                    seen_started = True
                elif (seen_started or (time.time() - t0 > 4.0)) and not pending and (
                    is_running is False or status in ("idle", "completed", "ready", "stopped", "error", "failed")
                ):
                    break

            transcript_md = client.req_text("GET", f"/v1/sessions/{sess_id}/export")
            (out_dir / f"{label}-transcript.md").write_text(transcript_md, encoding="utf-8")
            (out_dir / f"{label}-session.json").write_text(
                json.dumps(last_detail, indent=2, sort_keys=True) + "\n", encoding="utf-8"
            )
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
            srv_log.close()
            prov_srv.shutdown()

        elapsed_s = round(time.time() - t0, 2)

        # Copy persisted receipts from cfg_dir (portable-python/receipts/*).
        receipts_out = out_dir / f"{label}-receipts"
        receipts_out.mkdir(parents=True, exist_ok=True)
        exec_records = []
        for p in cfg_dir.rglob("executions.jsonl"):
            shutil.copy2(p, receipts_out / "executions.jsonl")
            for line in p.read_text(encoding="utf-8").splitlines():
                if line.strip():
                    exec_records.append(json.loads(line))
        for fname in ("admissions.jsonl", "publications.jsonl"):
            for p in cfg_dir.rglob(fname):
                shutil.copy2(p, receipts_out / fname)

        # Copy produced workbook and verify independently.
        xlsx_src = ws / "repositories.xlsx"
        workbook_check = verify_workbook_independent(xlsx_src)
        shutil.copy2(xlsx_src, out_dir / f"{label}-repositories.xlsx")

        # Verify tool result + persisted receipts confirm clean execution on native-aot + pinned image.
        tool_result_text = tool_result_file.read_text(encoding="utf-8") if tool_result_file.exists() else ""
        tool_result_json = None
        for line in tool_result_text.splitlines():
            s = line.strip()
            if s.startswith("{") and '"execution_id"' in s:
                try:
                    tool_result_json = json.loads(s)
                    break
                except Exception:
                    pass
        if tool_result_json is None:
            try:
                tool_result_json = json.loads(tool_result_text)
            except Exception:
                pass

        completed_recs = [r for r in exec_records if r.get("clean_completion") is True and r.get("exit_code") == 0]
        if not completed_recs:
            raise AssertionError(f"no clean_completion execution receipt found in {receipts_out / 'executions.jsonl'}")
        last_rec = completed_recs[-1]
        if last_rec.get("runtime_image_id") != image_id:
            raise AssertionError(f"receipt runtime_image_id={last_rec.get('runtime_image_id')} != staged image_id={image_id}")

        if not isinstance(tool_result_json, dict):
            raise AssertionError(f"could not parse JSON ToolResponse from tool_result_text: {tool_result_text[:300]!r}")
        observed_backend = str(tool_result_json.get("backend") or "")
        observed_engine_tag = str(tool_result_json.get("engine_tag") or "")
        if observed_backend != "native-aot":
            raise AssertionError(f"tool response backend={observed_backend!r} want 'native-aot'")
        if observed_engine_tag != EXPECTED_ENGINE_TAG:
            raise AssertionError(f"tool response engine_tag={observed_engine_tag!r} want {EXPECTED_ENGINE_TAG!r}")
        if tool_result_json.get("clean_completion") is not True or tool_result_json.get("exit_code") != 0:
            raise AssertionError(f"tool response did not complete cleanly: {tool_result_json}")

        verdict = {
            "status": "PASS",
            "label": label,
            "elapsed_s": elapsed_s,
            "platform": f"{platform.system().lower()}-{platform.machine().lower()}",
            "binary_path": str(binary),
            "binary_sha256": sha256_file(binary),
            "engine_root": str(engine_root),
            "pw_portable_python_image_dir": "UNSET",
            "pw_portable_python_engine_dir": "UNSET",
            "staged_engine": {
                "engine_tag": engine_manifest["engine_tag"],
                "source_commit": engine_manifest["source_commit"],
                "abi_version": engine_manifest["abi_version"],
                "library_name": lib_name,
                "library_sha256": actual_lib_sha,
            },
            "staged_image": {
                "image_id": image_id,
                "image_manifest_sha256": sha256_file(image_manifest_path),
                "has_bytecode_section": "bytecode" in image_manifest or bool(list((engine_root / "portable-python" / "image").rglob("*.pyc"))),
            },
            "tool_invocation": {
                "provider_calls": prov_state["calls"],
                "observed_backend": observed_backend or "native-aot (default-route verified by staged engine manifest + receipt)",
                "execution_id": last_rec.get("execution_id"),
                "receipt_runtime_image_id": last_rec.get("runtime_image_id"),
                "receipt_clean_completion": last_rec.get("clean_completion"),
            },
            "workbook_readback": workbook_check,
        }
        verdict_path = out_dir / f"{label}-verdict.json"
        verdict_path.write_text(json.dumps(verdict, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"PASS [{label}]: workbook readback + native-aot identity verified ({elapsed_s}s) -> {verdict_path}")
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    main()
