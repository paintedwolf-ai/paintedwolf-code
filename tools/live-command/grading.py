#!/usr/bin/env python3
"""Deterministic artifact grader, receipt analyzer, and path scrubber."""

from __future__ import annotations

import collections
import json
from pathlib import Path
import re
import sqlite3

HOME_RE = re.compile(r"/(?:Users|home)/[^/\s\"'`]+")
TMP_MAC_RE = re.compile(r"/private/var/folders/[^/\s\"'`]+/[^/\s\"'`]+/T")
BEARER_RE = re.compile(r"(Bearer\s+)[A-Za-z0-9._-]+")
CMD_JSON_RE = re.compile(r'\{"program_id"\s*:.*?\}(?=\s*$|\s*\n)', re.S)
OAR_CODE_RE = re.compile(r'"code"\s*:\s*"([A-Z][A-Z0-9_]{2,})"')
TEXT_CODE_RE = re.compile(r"\bCode:\s*([A-Z][A-Z0-9_]{2,})\b")


def scrub_text(text: str) -> str:
    """Replace local user/temp paths and bearer tokens with portable placeholders."""
    if not text:
        return ""
    out = TMP_MAC_RE.sub("$TMPDIR", text)
    out = HOME_RE.sub("$HOME", out)
    return BEARER_RE.sub(r"\1[REDACTED]", out)


def scrub_obj(value):
    if isinstance(value, str):
        return scrub_text(value)
    if isinstance(value, list):
        return [scrub_obj(v) for v in value]
    if isinstance(value, dict):
        return {scrub_text(str(k)): scrub_obj(v) for k, v in value.items()}
    return value


def extract_case_evidence(case_dir: Path) -> dict:
    """Parse messages, invocations, and program receipts captured for one case."""
    msgs_path = case_dir / "messages.json"
    rec_path = case_dir / "receipts.json"
    messages = json.loads(msgs_path.read_text()).get("messages", []) if msgs_path.exists() else []
    receipts = json.loads(rec_path.read_text()) if rec_path.exists() else {}

    assistant_texts = []
    tool_calls = []
    command_outputs = []
    refusal_codes = []
    native_gaps = []

    for msg in messages:
        role = msg.get("role")
        if role == "assistant":
            if msg.get("content"):
                assistant_texts.append(msg["content"])
            for tc in msg.get("tool_calls") or []:
                tool_calls.append({"name": tc.get("name"), "args": tc.get("args") or tc.get("arguments") or {}})
        elif role == "tool":
            tr = msg.get("tool_result") or {}
            raw = tr.get("content") or msg.get("content") or ""
            for code in OAR_CODE_RE.findall(raw) + TEXT_CODE_RE.findall(raw):
                if code != " COMPATIBLE":
                    refusal_codes.append(code)
            m = CMD_JSON_RE.search(raw)
            if m:
                try:
                    parsed = json.loads(m.group(0))
                    command_outputs.append(parsed)
                    if parsed.get("stop_code"):
                        refusal_codes.append(parsed["stop_code"])
                    ref = parsed.get("refusal") or {}
                    if ref.get("code"):
                        refusal_codes.append(ref["code"])
                except Exception:
                    pass

    exec_count = 0
    refused_children = []
    program_states = []
    for pid, rdata in receipts.items():
        if not isinstance(rdata, dict) or "program" not in rdata:
            continue
        prog = rdata.get("program") or {}
        pstate = prog.get("state", "unknown")
        pstop = prog.get("stop_code") or ""
        program_states.append({"program_id": pid, "state": pstate, "stop_code": pstop})
        if pstop:
            refusal_codes.append(pstop)
        for ex in rdata.get("executions") or []:
            exec_count += 1
            gap = ex.get("native_gap")
            if isinstance(gap, dict) and gap.get("command"):
                native_gaps.append(
                    {
                        "command": gap.get("command", ""),
                        "code": gap.get("code", ""),
                        "form": gap.get("form", ""),
                    }
                )
            estop = ex.get("stop_code") or ex.get("host_stop_code") or ""
            if ex.get("state") in ("refused", "rejected") or estop:
                cmd_str = scrub_text((ex.get("command") or ex.get("canonical") or "")[:120])
                refused_children.append({"command": cmd_str, "code": estop or ex.get("state", "")})
                if estop:
                    refusal_codes.append(estop)

    combined_stdout = "\n".join(c.get("stdout", "") for c in command_outputs)
    assistant_body = "\n".join(assistant_texts)
    final_reply = assistant_texts[-1] if assistant_texts else ""
    return {
        "assistant_texts": assistant_texts,
        "assistant_body": assistant_body,
        "final_reply": final_reply,
        "tool_calls": tool_calls,
        "command_calls": [tc for tc in tool_calls if tc.get("name") == "command"],
        "command_outputs": command_outputs,
        "combined_stdout": combined_stdout,
        "program_states": program_states,
        "execution_count": exec_count,
        "native_gaps": native_gaps,
        "refused_children": refused_children,
        "refusal_codes": refusal_codes,
    }


def analyze_store_db(db_path: Path, session_ids: list[str] | None = None) -> dict:
    """Read-only SQLite analysis of native_gap_json and refused executions per session."""
    if not db_path.exists():
        return {"per_session": {}, "native_gaps": [], "refused_children": [], "program_states": []}
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        query = (
            "SELECT p.session_id, e.program_id, e.ordinal, e.canonical, e.state, "
            "e.owner, e.stop_code, e.host_stop_code, e.native_gap_json, e.status_code "
            "FROM invocation_program_executions e "
            "JOIN invocation_programs g ON g.id = e.program_id "
            "JOIN invocation_receipts p ON p.id = g.parent_receipt_id"
        )
        rows = conn.execute(query).fetchall()
        gaps_counter = collections.Counter()
        refusals_counter = collections.Counter()
        per_session = {}
        allowed = set(session_ids) if session_ids else None
        for sid, _pid, _ord, canon, state, _owner, stop, hstop, gap_json, _status in rows:
            if allowed is not None and sid not in allowed:
                continue
            slot = per_session.setdefault(sid, {"children": 0, "gaps": 0, "refused": 0, "gap_items": [], "refusal_items": []})
            slot["children"] += 1
            if gap_json:
                g = json.loads(gap_json)
                key = (g.get("command") or "", g.get("code") or "", g.get("form") or "")
                gaps_counter[key] += 1
                slot["gaps"] += 1
                slot["gap_items"].append({"command": key[0], "code": key[1], "form": key[2]})
            if state in ("refused", "rejected") or stop or hstop:
                code = stop or hstop or state
                cmd_short = scrub_text((canon or "")[:80])
                refusals_counter[(cmd_short, code)] += 1
                slot["refused"] += 1
                slot["refusal_items"].append({"command": cmd_short, "code": code})

        p_rows = conn.execute(
            "SELECT p.session_id, g.state, g.stop_code, count(*) "
            "FROM invocation_programs g JOIN invocation_receipts p ON p.id = g.parent_receipt_id "
            "GROUP BY 1, 2, 3"
        ).fetchall()
        prog_states = [
            {"session_id": r[0], "state": r[1], "stop_code": r[2] or "", "count": r[3]}
            for r in p_rows
            if allowed is None or r[0] in allowed
        ]
        return {
            "per_session": per_session,
            "program_states": prog_states,
            "native_gaps": [
                {"command": k[0], "code": k[1], "form": k[2], "count": v}
                for k, v in gaps_counter.most_common()
            ],
            "refused_children": [
                {"command": k[0], "code": k[1], "count": v}
                for k, v in refusals_counter.most_common()
            ],
        }
    finally:
        conn.close()


def grade_live_task(label: str, project_dir: Path, case_dir: Path) -> dict:
    """Deterministically grade a completed live task attempt from disk state and receipts."""
    ev = extract_case_evidence(case_dir)
    cmd_called = len(ev["command_calls"]) > 0
    reply_low = ev["assistant_body"].lower()
    corpus = (ev["combined_stdout"] + "\n" + reply_low).lower()
    reasons = []
    if not cmd_called:
        reasons.append("model did not invoke the command tool")

    artifact_ok = True
    response_ok = True

    if label.startswith("L01"):
        if "tool.go" not in reply_low or "28" not in reply_low:
            response_ok = False
            reasons.append("reply missing cmd/tool/tool.go (28 lines)")
        if "6" not in reply_low:
            response_ok = False
            reasons.append("reply missing total Go file count (6)")
    elif label.startswith("L02"):
        broken_src = (project_dir / "internal" / "broken" / "broken.go").read_text() if (project_dir / "internal" / "broken" / "broken.go").exists() else ""
        if "n*2 + 1" not in broken_src:
            artifact_ok = False
            reasons.append("broken.go was modified despite instruction not to fix")
        if "testdouble" not in reply_low or ("9" not in reply_low and "+ 1" not in reply_low and "+1" not in reply_low):
            response_ok = False
            reasons.append("reply did not explain TestDouble failure and n*2+1 cause")
    elif label.startswith("L03"):
        notes = project_dir / "notes.txt"
        if not notes.exists():
            artifact_ok = False
            reasons.append("notes.txt was not created on disk")
        else:
            txt = notes.read_text()
            for expected in ("tool.go", "legacy.js", "broken.go", "calc.go", "main.go"):
                if expected not in txt:
                    artifact_ok = False
                    reasons.append(f"notes.txt missing {expected}")
    elif label.startswith("L04"):
        cfg_file = project_dir / "config.json"
        try:
            cfg = json.loads(cfg_file.read_text())
            if cfg.get("y") != 42 or "x" in cfg or cfg.get("name") != "sanity":
                artifact_ok = False
                reasons.append(f"config.json unexpected content: {cfg}")
        except Exception as exc:
            artifact_ok = False
            reasons.append(f"config.json invalid JSON: {exc}")
    elif label.startswith("L05"):
        cmds = " ".join(c["args"].get("command", "") for c in ev["command_calls"])
        tool_names = {tc.get("name") for tc in ev["tool_calls"]}
        stopped = ("kill" in cmds) or ("command_stop" in tool_names)
        if "sleep" not in cmds or not stopped:
            response_ok = False
            reasons.append("task did not both start sleep and stop/kill it")
    elif label.startswith("L06"):
        if "big.bin" not in reply_low or "medium.dat" not in reply_low or "small.dat" in reply_low:
            response_ok = False
            reasons.append("reply must list big.bin and medium.dat and exclude small.dat")
        if "big.bin" in reply_low and "medium.dat" in reply_low:
            if reply_low.index("big.bin") > reply_low.index("medium.dat"):
                response_ok = False
                reasons.append("big.bin not listed before medium.dat")
    elif label.startswith("L07"):
        for count_str in ("66", "33", "21", "26", "22"):
            if count_str not in reply_low:
                response_ok = False
                reasons.append(f"reply missing expected log stat count {count_str}")
    elif label.startswith("L08"):
        list_file = project_dir / "docs" / "nested" / "list.txt"
        if not list_file.exists():
            artifact_ok = False
            reasons.append("docs/nested/list.txt missing")
        else:
            lines = [ln.strip() for ln in list_file.read_text().splitlines() if ln.strip()]
            if lines != ["ALPHA", "beta", "end"]:
                artifact_ok = False
                reasons.append(f"docs/nested/list.txt lines={lines}, expected ['ALPHA', 'beta', 'end']")
    elif label.startswith("L09"):
        if "readme.txt" not in reply_low or ("edited" not in corpus and "initial" not in corpus):
            response_ok = False
            reasons.append("reply missing git status/diff summary for README.txt")
    elif label.startswith("L10"):
        if "2.4.1" not in reply_low or "sanity" not in reply_low:
            response_ok = False
            reasons.append("reply missing status.json fields (sanity, 2.4.1)")

    passed = cmd_called and artifact_ok and response_ok
    if not passed and not reasons:
        reasons.append("task assertions did not pass")
    return {
        "label": label,
        "passed": passed,
        "command_invoked": cmd_called,
        "artifact_ok": artifact_ok,
        "response_ok": response_ok,
        "reasons": reasons,
        "programs_count": len(ev["program_states"]),
        "executions_count": ev["execution_count"],
        "native_gaps": ev["native_gaps"],
        "refusal_codes": ev["refusal_codes"],
        "refused_children": ev["refused_children"],
    }
