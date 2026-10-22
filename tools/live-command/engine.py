#!/usr/bin/env python3
"""Isolated lycaon-dev engine lifecycle, credential vault staging, and API helpers.

Never touches 127.0.0.1:8787 or the shared development store, except read-only
copying of encrypted vault files into an isolated per-run config directory.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import time
import urllib.error
import urllib.request
import uuid

from fixture import probe_free_port

CREDENTIAL_FILES = ("providers.local.yaml", "credential-vault.age", ".credential-vault-development-identity")
DANGEROUS_CHECKPOINT_KEYS = ("process_signal", "process_list", "process_kill")
SIBLING_WORKTREES = ("live-sanity-2", "portable-command-external-execution", "live-sanity-1")


def default_worktree() -> Path:
    if os.environ.get("PW_WORKTREE"):
        return Path(os.environ["PW_WORKTREE"]).resolve()
    for parent in Path(__file__).resolve().parents:
        if (parent / "Taskfile.yml").exists() and (parent / "lycaon").is_dir():
            return parent
    return Path.cwd().resolve()


def default_dev_config() -> Path:
    if os.environ.get("PW_DEV_CONFIG_DIR"):
        return Path(os.environ["PW_DEV_CONFIG_DIR"]).expanduser().resolve()
    return Path.home() / ".config" / "paintedwolf-dev"


def _candidate_worktrees(wt: Path) -> list[Path]:
    cands = [wt]
    if wt.parent.name == "worktrees":
        cands.extend(wt.parent / n for n in SIBLING_WORKTREES if (wt.parent / n).is_dir() and (wt.parent / n) != wt)
    return cands


def resolve_engine_binary(worktree: Path | None = None, build_if_missing: bool = False) -> Path:
    if os.environ.get("PW_ENGINE"):
        return Path(os.environ["PW_ENGINE"]).resolve()
    wt = worktree or default_worktree()
    for cand in _candidate_worktrees(wt):
        script = cand / "scripts" / "artifact_paths.py"
        if not script.exists():
            continue
        try:
            bdir = subprocess.check_output(["python3", str(script), "build"], cwd=cand, text=True).strip()
            if (Path(bdir) / "lycaon-dev").exists():
                return Path(bdir) / "lycaon-dev"
        except Exception:
            continue
    if build_if_missing:
        subprocess.run(["./task", "build:lycaon-dev"], cwd=wt, check=True)
        bdir = subprocess.check_output(["python3", str(wt / "scripts/artifact_paths.py"), "build"], cwd=wt, text=True).strip()
        return Path(bdir) / "lycaon-dev"
    raise FileNotFoundError("lycaon-dev binary not found; set PW_ENGINE or build with ./task build:lycaon-dev")


def resolve_engine_root(worktree: Path | None = None) -> Path:
    if os.environ.get("PW_ENGINE_ROOT"):
        return Path(os.environ["PW_ENGINE_ROOT"]).resolve()
    wt = worktree or default_worktree()
    for cand in _candidate_worktrees(wt):
        er = cand / "lycaon-den" / "src-tauri" / "engine-root"
        if er.is_dir():
            return er
    return wt / "lycaon-den" / "src-tauri" / "engine-root"


def is_dangerous_checkpoint(checkpoint: dict) -> bool:
    blob = json.dumps(checkpoint)
    return any(key in blob for key in DANGEROUS_CHECKPOINT_KEYS)


def stage_credentials(dev_dir: Path, cfg_dir: Path, provider_id: str, model: str):
    """Copy encrypted vault and provider config with 0600 permissions without printing contents."""
    cfg_dir.mkdir(parents=True, exist_ok=True)
    cfg_dir.chmod(0o700)
    for fname in CREDENTIAL_FILES:
        src, dst = dev_dir / fname, cfg_dir / fname
        if not src.exists():
            raise FileNotFoundError(f"missing required credential file: {fname}")
        shutil.copyfile(src, dst)
        dst.chmod(0o600)
    policy = (
        f"coordinator:\n    provider_id: {provider_id}\n    model: {model}\n"
        f"lite:\n    provider_id: {provider_id}\n    model: {model}\n"
        f"agent_pool:\n    selection: first\n    models:\n        - provider_id: {provider_id}\n          model: {model}\n"
    )
    policy_path = cfg_dir / "model-policy.yaml"
    policy_path.write_text(policy)
    policy_path.chmod(0o600)


def scrubbed_env(instance_dir: Path, cfg_dir: Path, port: int, token: str, mode: str, engine_root: Path) -> dict:
    for sub in ("home", "tmp", "debug"):
        (instance_dir / sub).mkdir(parents=True, exist_ok=True)
    env = {
        "HOME": str(instance_dir / "home"), "USER": "pwtester", "LOGNAME": "pwtester",
        "SHELL": "/bin/sh", "LANG": "en_US.UTF-8", "PATH": "/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
        "TMPDIR": str(instance_dir / "tmp") + "/", "GOTOOLCHAIN": "local", "PW_CANARY_SECRET": "canary-not-a-real-secret",
        "LYCAON_CONFIG_DIR": str(cfg_dir), "LYCAON_ADDR": f"127.0.0.1:{port}", "LYCAON_API_TOKEN": token,
        "LYCAON_ENGINE_ROOT": str(engine_root), "LYCAON_DEV": "1", "LYCAON_LOG_LEVEL": "debug",
        "LYCAON_DEBUG_SESSION_DIR": str(instance_dir / "debug"), "LYCAON_LLM_DEBUG": "1",
        "LYCAON_TOOL_DEBUG": "1", "LYCAON_HTTP_DEBUG": "1", "LYCAON_LLM_MOCK": "0",
    }
    if mode == "manual":
        env.update({"LYCAON_LLM_MOCK": "1", "LYCAON_LLM_MANUAL": "1", "LYCAON_HARNESS": "1"})
    return env


class IsolatedEngine:
    """Manages an isolated lycaon-dev engine process and its recorded PID."""

    def __init__(self, run_dir: Path, name: str, worktree: Path | None = None, dev_dir: Path | None = None):
        self.run_dir = Path(run_dir).resolve()
        self.name = name
        self.instance_dir = self.run_dir / name
        self.worktree = worktree or default_worktree()
        self.dev_dir = dev_dir or default_dev_config()

    @property
    def ownership_path(self) -> Path:
        return self.instance_dir / "ownership.json"

    def load_state(self) -> dict:
        return json.loads(self.ownership_path.read_text())

    def save_state(self, state: dict):
        self.instance_dir.mkdir(parents=True, exist_ok=True)
        self.ownership_path.write_text(json.dumps(state, indent=2))
        self.ownership_path.chmod(0o600)

    def log_pid_event(self, action: str, pid: int, port: int | None = None):
        self.run_dir.mkdir(parents=True, exist_ok=True)
        suffix = f" port={port}" if port is not None else ""
        with open(self.run_dir / "pids.log", "a") as fh:
            fh.write(f"{time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())} {action} {self.name} pid={pid}{suffix}\n")

    def start(self, mode: str = "live", provider_id: str = "together-ai-1", model: str = "zai-org/GLM-5.3-Flash", external: str = "offered", restart: bool = False) -> dict:
        cfg_dir = self.instance_dir / "cfg"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        cfg_dir.chmod(0o700)
        if mode == "live" and not restart:
            stage_credentials(self.dev_dir, cfg_dir, provider_id, model)
        (cfg_dir / "command-programs.yaml").write_text(f"external_selector: {external}\n")
        if restart and self.ownership_path.exists():
            old = self.load_state()
            port, token = old["port"], old["token"]
        else:
            port, token = probe_free_port(), secrets.token_hex(24)
        if port == 8787:
            port = probe_free_port()
        env = scrubbed_env(self.instance_dir, cfg_dir, port, token, mode, resolve_engine_root(self.worktree))
        binary = resolve_engine_binary(self.worktree)
        log_fh = open(self.instance_dir / "engine.log", "a")
        proc = subprocess.Popen(
            [str(binary), "serve", "--db", str(self.instance_dir / "store.db")],
            cwd=str(self.worktree), env=env, stdout=log_fh, stderr=subprocess.STDOUT, start_new_session=True,
        )
        state = self.load_state() if (restart and self.ownership_path.exists()) else {}
        state.update({"pid": proc.pid, "port": port, "token": token, "mode": mode, "provider_id": provider_id, "model": model, "external": external, "binary": str(binary)})
        self.save_state(state)
        self.log_pid_event("start", proc.pid, port)
        for _ in range(240):
            try:
                self.req("/health")
                return {"pid": proc.pid, "port": port, "healthy": True}
            except Exception:
                if proc.poll() is not None:
                    raise RuntimeError(f"engine exited early with code {proc.returncode}")
                time.sleep(0.5)
        raise TimeoutError(f"engine pid={proc.pid} not healthy after 120s")

    def stop(self) -> bool:
        pid = self.load_state().get("pid") if self.ownership_path.exists() else None
        if not pid:
            return True
        try:
            os.kill(pid, 15)
        except ProcessLookupError:
            self.log_pid_event("already-stopped", pid)
            return True
        for _ in range(60):
            try:
                if os.waitpid(pid, os.WNOHANG)[0] == pid:
                    self.log_pid_event("stop", pid)
                    return True
            except ChildProcessError:
                pass
            try:
                os.kill(pid, 0)
                time.sleep(0.2)
            except ProcessLookupError:
                self.log_pid_event("stop", pid)
                return True
        return False

    def req(self, path: str, body=None, method: str | None = None, raw: bool = False, timeout: int = 60):
        state = self.load_state()
        request = urllib.request.Request(
            f"http://127.0.0.1:{state['port']}{path}",
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Authorization": "Bearer " + state["token"], "Content-Type": "application/json"},
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout) as resp:
                data = resp.read()
                return data.decode() if raw else (json.loads(data) if data else None)
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode(errors="replace")[:2000]
            raise RuntimeError(f"{method or ('POST' if body is not None else 'GET')} {path} -> {exc.code}: {detail}") from exc

    def setup_session(self, project_path: Path) -> dict:
        state = self.load_state()
        proj = self.req("/v1/projects", {"roots": [{"path": str(project_path)}]})
        body = {"project_id": proj["id"], "posture": "build"}
        if state.get("mode") == "live":
            body.update({"provider_id": state["provider_id"], "model": state["model"]})
        sess = self.req("/v1/sessions", body)
        status = "preparing"
        for _ in range(240):
            status = self.req("/v1/sessions/" + sess["id"]).get("status", "preparing")
            if status != "preparing":
                break
            time.sleep(0.5)
        state.update({"project_id": proj["id"], "session_id": sess["id"]})
        self.save_state(state)
        return {"project_id": proj["id"], "session_id": sess["id"], "status": status}

    def run_prompt(self, label: str, text: str, approve: str = "reject", timeout: float = 300.0) -> dict:
        sid = self.load_state()["session_id"]
        t0 = time.monotonic()
        self.req(f"/v1/sessions/{sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": text})
        deadline, seen_busy, resolved_cps, final_status = t0 + timeout, False, [], "unknown"
        while time.monotonic() < deadline:
            time.sleep(1.5)
            final_status = self.req("/v1/sessions/" + sid).get("status", "unknown")
            cps = self.req(f"/v1/sessions/{sid}/checkpoints").get("checkpoints", [])
            pending = [c for c in cps if c.get("status") in (None, "pending") and c.get("id") not in resolved_cps]
            if pending:
                cp = pending[0]
                (self.instance_dir / f"checkpoint-{cp['id']}.json").write_text(json.dumps(cp, indent=2))
                if approve == "ask":
                    break
                opts = ((cp.get("tool_approval") or {}).get("plan") or {}).get("options") or []
                if approve == "approve" and opts and not is_dangerous_checkpoint(cp):
                    payload = {"kind": "tool_approval", "action": "approve", "option_id": opts[0].get("id") or opts[0].get("option_id")}
                else:
                    payload = {"kind": "tool_approval", "action": "reject"}
                self.req(f"/v1/sessions/{sid}/checkpoints/{cp['id']}", payload)
                resolved_cps.append(cp["id"])
                continue
            if final_status == "busy":
                seen_busy = True
            if final_status in ("idle", "error") and (seen_busy or time.monotonic() > t0 + 20):
                break
        else:
            final_status = "timeout"
        return {"status": final_status, "elapsed_s": round(time.monotonic() - t0, 2), "session_id": sid, **self.dump_case(label, sid)}

    def dump_case(self, label: str, session_id: str | None = None) -> dict:
        sid = session_id or self.load_state()["session_id"]
        out = self.instance_dir / "cases" / label
        out.mkdir(parents=True, exist_ok=True)
        msgs = self.req(f"/v1/sessions/{sid}/messages?limit=200", raw=True)
        inv = self.req(f"/v1/sessions/{sid}/invocations?limit=500", raw=True)
        (out / "messages.json").write_text(msgs)
        (out / "invocations.json").write_text(inv)
        prog_ids = sorted(set(re.findall(r'"program_id\\?"\s*:\s*\\?"([0-9a-f-]{36})', msgs + inv)))
        receipts = {}
        for pid in prog_ids:
            try:
                receipts[pid] = self.req(f"/v1/sessions/{sid}/program-receipts/{pid}?limit=100")
            except Exception as exc:
                receipts[pid] = {"error": str(exc)}
        (out / "receipts.json").write_text(json.dumps(receipts, indent=2))
        return {"case_dir": str(out), "programs": prog_ids}
