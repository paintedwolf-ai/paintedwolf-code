#!/usr/bin/env python3
"""Multi-model live suite runner, manual case driver, and report generator."""

from __future__ import annotations

import collections
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import uuid

from engine import IsolatedEngine, default_worktree, is_dangerous_checkpoint
from fixture import build_fixture_template, clone_fixture, probe_free_port
from grading import CMD_JSON_RE, analyze_store_db, grade_live_task, scrub_obj, scrub_text


def model_slug(provider_id: str, model: str) -> str:
    return re.sub(r"[^a-z0-9._-]+", "-", f"{provider_id}--{model}".lower()).strip("-")


def start_loopback_httpd(run_dir: Path, port: int | None = None) -> tuple[subprocess.Popen, int]:
    actual_port = port or probe_free_port()
    run_dir.mkdir(parents=True, exist_ok=True)
    log_fh = open(run_dir / "httpd.log", "a")
    proc = subprocess.Popen(
        [sys.executable, str(Path(__file__).resolve().parent / "fixture.py"), "httpd", str(actual_port)],
        stdout=log_fh, stderr=subprocess.STDOUT, start_new_session=True,
    )
    (run_dir / "httpd.pid").write_text(str(proc.pid))
    with open(run_dir / "pids.log", "a") as fh:
        fh.write(f"{time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())} start httpd pid={proc.pid} port={actual_port}\n")
    time.sleep(0.3)
    return proc, actual_port


def stop_loopback_httpd(run_dir: Path, proc: subprocess.Popen | None = None):
    pid = proc.pid if proc is not None else None
    pid_file = run_dir / "httpd.pid"
    if pid is None and pid_file.exists():
        try:
            pid = int(pid_file.read_text().strip())
        except ValueError:
            pid = None
    if not pid:
        return
    try:
        os.kill(pid, 15)
        if proc is not None:
            proc.wait(timeout=5)
    except Exception:
        pass
    with open(run_dir / "pids.log", "a") as fh:
        fh.write(f"{time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())} stop httpd pid={pid}\n")


def record_attempt(attempts_list: list, attempt_record: dict) -> list:
    """Append an attempt in chronological order without filtering failures."""
    attempt_record["chronological_index"] = len(attempts_list) + 1
    attempts_list.append(attempt_record)
    return attempts_list


def aggregate_model_attempts(provider_id: str, model: str, attempts: list, db_summary: dict) -> dict:
    """Compute pass rates, native_gap tallies, and refusal tallies across all retained attempts."""
    total = len(attempts)
    passed = sum(1 for a in attempts if a.get("grade", {}).get("passed"))
    gap_counter = collections.Counter()
    refusal_counter = collections.Counter()
    for item in db_summary.get("native_gaps", []):
        gap_counter[(item.get("command", ""), item.get("code", ""), item.get("form", ""))] += item.get("count", 1)
    for item in db_summary.get("refused_children", []):
        refusal_counter[(item.get("command", ""), item.get("code", ""))] += item.get("count", 1)
    if not gap_counter:
        for att in attempts:
            for g in att.get("grade", {}).get("native_gaps", []):
                gap_counter[(g.get("command", ""), g.get("code", ""), g.get("form", ""))] += 1
    for att in attempts:
        for code in att.get("grade", {}).get("refusal_codes", []):
            refusal_counter[("oar_or_stop", code)] += 1
    return {
        "provider_id": provider_id,
        "model": model,
        "total_attempts": total,
        "passed_attempts": passed,
        "failed_attempts": total - passed,
        "pass_rate": round(passed / total, 4) if total else 0.0,
        "attempts": attempts,
        "native_gaps": [{"command": k[0], "code": k[1], "form": k[2], "count": v} for k, v in gap_counter.most_common()],
        "refusals": [{"source": k[0], "code": k[1], "count": v} for k, v in refusal_counter.most_common()],
        "program_states": db_summary.get("program_states", []),
    }


def _run_single_model(run_dir: Path, wt: Path, template_dir: Path, httpd_port: int, mspec: dict, tasks: list[dict], attempts_per_task: int, timeout_s: float) -> dict:
    provider_id, model_id = mspec["provider_id"], mspec["model"]
    eng = IsolatedEngine(run_dir, f"live-{model_slug(provider_id, model_id)}", worktree=wt)
    attempts_log, session_ids = [], []
    db_sum = {"per_session": {}, "native_gaps": [], "refused_children": [], "program_states": []}
    try:
        eng.start(mode="live", provider_id=provider_id, model=model_id)
        for task in tasks:
            t_label = task["label"]
            prompt_text = re.sub(r"http://127\.0\.0\.1:\d+/", f"http://127.0.0.1:{httpd_port}/", task["text"])
            for att_num in range(1, attempts_per_task + 1):
                case_label = f"{t_label}--a{att_num}" if attempts_per_task > 1 else t_label
                proj_dir = clone_fixture(template_dir, eng.instance_dir / "projects" / case_label)
                started_iso = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
                try:
                    sid = eng.setup_session(proj_dir)["session_id"]
                    session_ids.append(sid)
                    run_info = eng.run_prompt(case_label, prompt_text, approve="reject", timeout=timeout_s)
                    grade = grade_live_task(t_label, proj_dir, Path(run_info["case_dir"]))
                    rec = {"task": t_label, "attempt": att_num, "started_at": started_iso, "status": run_info["status"], "elapsed_s": run_info["elapsed_s"], "session_id": sid, "programs": run_info["programs"], "grade": grade}
                    if run_info["status"] == "timeout":
                        eng.stop()
                        eng.start(mode="live", provider_id=provider_id, model=model_id, restart=True)
                except Exception as exc:
                    err = scrub_text(str(exc))
                    rec = {
                        "task": t_label, "attempt": att_num, "started_at": started_iso, "status": "error", "elapsed_s": 0.0,
                        "session_id": "", "programs": [], "error": err,
                        "grade": {"label": t_label, "passed": False, "command_invoked": False, "artifact_ok": False, "response_ok": False, "reasons": [f"infrastructure error: {err}"], "programs_count": 0, "executions_count": 0, "native_gaps": [], "refusal_codes": [], "refused_children": []},
                    }
                record_attempt(attempts_log, rec)
                print(
                    f"[{provider_id}:{model_id}] {t_label}#{att_num} status={rec['status']} "
                    f"passed={rec['grade']['passed']} elapsed={rec['elapsed_s']}s",
                    flush=True,
                )
        db_sum = analyze_store_db(eng.instance_dir / "store.db", session_ids)
        for att in attempts_log:
            ps = db_sum.get("per_session", {}).get(att.get("session_id"), {})
            if ps.get("gap_items"):
                att["grade"]["native_gaps"] = ps["gap_items"]
    finally:
        eng.stop()
    return aggregate_model_attempts(provider_id, model_id, attempts_log, db_sum)


def run_live_suite(run_dir: Path, models: list[dict], tasks: list[dict], attempts_per_task: int = 1, timeout_s: float = 240.0, worktree: Path | None = None, parallel_models: int = 1) -> dict:
    """Run the fixed live task suite across models on isolated engines and grade results."""
    run_dir = Path(run_dir).resolve()
    run_dir.mkdir(parents=True, exist_ok=True)
    wt = worktree or default_worktree()
    template_dir = build_fixture_template(run_dir / "fixture-src")
    httpd_proc, httpd_port = start_loopback_httpd(run_dir)
    try:
        workers = max(1, min(parallel_models, len(models)))
        if workers > 1:
            with ThreadPoolExecutor(max_workers=workers) as pool:
                futures = [pool.submit(_run_single_model, run_dir, wt, template_dir, httpd_port, m, tasks, attempts_per_task, timeout_s) for m in models]
                model_results = [f.result() for f in futures]
        else:
            model_results = [_run_single_model(run_dir, wt, template_dir, httpd_port, m, tasks, attempts_per_task, timeout_s) for m in models]
    finally:
        stop_loopback_httpd(run_dir, httpd_proc)
    try:
        commit = subprocess.check_output(["git", "-C", str(wt), "rev-parse", "--short", "HEAD"], text=True).strip()
    except Exception:
        commit = "unknown"
    summary = scrub_obj({"generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "source_commit": commit, "attempts_per_task": attempts_per_task, "task_count": len(tasks), "models": model_results})
    (run_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    (run_dir / "report.md").write_text(render_markdown_report(summary))
    return summary


def run_manual_cases(run_dir: Path, cases: list[dict], worktree: Path | None = None) -> dict:
    """Run deterministic command cases through the manual mock provider."""
    run_dir = Path(run_dir).resolve()
    wt = worktree or default_worktree()
    proj_dir = clone_fixture(run_dir / "fixture-src", run_dir / "dmanual-project")
    eng = IsolatedEngine(run_dir, "dmanual", worktree=wt)
    results = []
    try:
        eng.start(mode="manual")
        sid = eng.setup_session(proj_dir)["session_id"]
        eng.req("/harness/llm/auto", {"enabled": False})
        for case in cases:
            rec = {"label": case["label"], "command": case["command"]}
            eng.req(f"/v1/sessions/{sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": "case " + case["label"]})
            pend = next((p for _ in range(10) if (p := eng.req(f"/harness/llm/pending?session_id={sid}&wait=20000")).get("pending")), None)
            if not pend:
                rec["error"] = "no pending model request"
                results.append(rec)
                continue
            if "command" not in (pend.get("tools") or []):
                eng.req("/harness/llm/respond", {"id": pend["id"], "content": "", "tool_calls": [{"name": "request_tools", "id": "call_" + uuid.uuid4().hex[:12], "args": {"need": "command"}}]})
                pend = eng.req(f"/harness/llm/pending?session_id={sid}&wait=30000")
            args = {k: case[k] for k in ("command", "cwd") if case.get(k)}
            eng.req("/harness/llm/respond", {"id": pend["id"], "content": "", "tool_calls": [{"name": "command", "id": "call_" + uuid.uuid4().hex[:12], "args": args}]})
            t0, nxt, resolved = time.monotonic(), None, set()
            while time.monotonic() - t0 < 120:
                nxt = eng.req(f"/harness/llm/pending?session_id={sid}&wait=3000")
                if nxt.get("pending"):
                    break
                cps = eng.req(f"/v1/sessions/{sid}/checkpoints").get("checkpoints", [])
                for cp in [c for c in cps if c.get("status") in (None, "pending") and c["id"] not in resolved]:
                    opts = ((cp.get("tool_approval") or {}).get("plan") or {}).get("options") or []
                    if case.get("approve", "approve") == "approve" and opts and not is_dangerous_checkpoint(cp):
                        body = {"kind": "tool_approval", "action": "approve", "option_id": opts[0].get("id") or opts[0].get("option_id")}
                    else:
                        body = {"kind": "tool_approval", "action": "reject"}
                    eng.req(f"/v1/sessions/{sid}/checkpoints/{cp['id']}", body)
                    resolved.add(cp["id"])
            rec["elapsed_s"] = round(time.monotonic() - t0, 2)
            if nxt and nxt.get("pending"):
                tmsgs = [m for m in nxt.get("messages", []) if m.get("role") == "tool"]
                raw_tr = tmsgs[-1].get("content", "") if tmsgs else ""
                rec["tool_result"] = scrub_text(raw_tr)
                if m := CMD_JSON_RE.search(raw_tr):
                    rec["program"] = json.loads(m.group(0))
                eng.req("/harness/llm/respond", {"id": nxt["id"], "content": "ok"})
            for _ in range(60):
                if eng.req("/v1/sessions/" + sid).get("status") == "idle":
                    break
                time.sleep(0.5)
            results.append(rec)
        db_sum = analyze_store_db(eng.instance_dir / "store.db", [sid])
    finally:
        eng.stop()
    out = scrub_obj({"cases": results, "db_summary": db_sum})
    (run_dir / "manual-summary.json").write_text(json.dumps(out, indent=2) + "\n")
    return out


def render_markdown_report(summary: dict) -> str:
    """Render a screened Markdown report from a suite summary."""
    lines = [
        f"# Portable command environment live multi-model report ({summary.get('source_commit', 'unknown')})",
        "",
        f"- Generated at: `{summary.get('generated_at', '')}`",
        f"- Tasks per model: `{summary.get('task_count', 0)}`",
        f"- Attempts per task: `{summary.get('attempts_per_task', 1)}` (all attempts retained in chronological order)",
        "",
        "## Model summary",
        "",
        "| Provider | Model | Passed | Total | Pass rate | Native gaps | Refusals |",
        "|---|---|---:|---:|---:|---:|---:|",
    ]
    for m in summary.get("models", []):
        ng_total = sum(g.get("count", 1) for g in m.get("native_gaps", []))
        rf_total = sum(r.get("count", 1) for r in m.get("refusals", []))
        lines.append(f"| `{m['provider_id']}` | `{m['model']}` | {m['passed_attempts']} | {m['total_attempts']} | {m.get('pass_rate', 0.0) * 100:.1f}% | {ng_total} | {rf_total} |")
    for m in summary.get("models", []):
        lines.extend(["", f"## `{m['provider_id']}` / `{m['model']}`", "", "### Task attempts", "", "| # | Task | Attempt | Status | Elapsed (s) | Programs | Executions | Grade | Notes |", "|---:|---|---:|---|---:|---:|---:|---|---|"])
        for att in m.get("attempts", []):
            gr = att.get("grade", {})
            verdict = "PASS" if gr.get("passed") else "FAIL"
            notes = "; ".join(gr.get("reasons", [])) or "ok"
            lines.append(f"| {att.get('chronological_index', 0)} | `{att.get('task')}` | {att.get('attempt', 1)} | `{att.get('status')}` | {att.get('elapsed_s', 0.0):.2f} | {gr.get('programs_count', 0)} | {gr.get('executions_count', 0)} | **{verdict}** | {scrub_text(notes)} |")
        lines.extend(["", "### Native gaps (`native_gap` facts)", ""])
        if m.get("native_gaps"):
            for g in m["native_gaps"]:
                form_suffix = f" (`{g['form']}`)" if g.get("form") else ""
                lines.append(f"- `{g['command']}`: `{g['code']}`{form_suffix} — {g.get('count', 1)}")
        else:
            lines.append("- None recorded.")
        lines.extend(["", "### Refusals and stop codes", ""])
        lines.extend([f"- `{r['code']}` (`{r['source']}`) — {r.get('count', 1)}" for r in m.get("refusals", [])] or ["- None recorded."])
    return "\n".join(lines) + "\n"
