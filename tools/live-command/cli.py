#!/usr/bin/env python3
"""Unified CLI for the portable command environment live multi-model harness."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

from engine import IsolatedEngine
from grading import CMD_JSON_RE, OAR_CODE_RE, analyze_store_db, scrub_text
from runner import render_markdown_report, run_live_suite, run_manual_cases

DEFAULT_MODELS = [
    {"provider_id": "together-ai-1", "model": "zai-org/GLM-5.3-Flash"},
    {"provider_id": "ollama-2", "model": "ministral-3:3b"},
    {"provider_id": "together-ai-1", "model": "Qwen/Qwen3.5-9B"},
]


def parse_models_arg(raw: str | None) -> list[dict]:
    if not raw:
        return list(DEFAULT_MODELS)
    specs = []
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        if ":" not in item:
            raise ValueError(f"model spec must be provider_id:model, got {item!r}")
        provider_id, model = item.split(":", 1)
        if "claude" in model.lower() or "anthropic" in provider_id.lower():
            raise ValueError("Claude models are forbidden in this harness")
        specs.append({"provider_id": provider_id, "model": model})
    return specs


def cmd_suite(args):
    tasks_path = Path(args.tasks) if args.tasks else HERE / "tasks" / "live-tasks.json"
    tasks = json.loads(tasks_path.read_text())
    if args.only:
        prefixes = [p.strip() for p in args.only.split(",") if p.strip()]
        tasks = [t for t in tasks if any(t["label"].startswith(p) for p in prefixes)]
    models = parse_models_arg(args.models)
    summary = run_live_suite(
        run_dir=Path(args.run_dir),
        models=models,
        tasks=tasks,
        attempts_per_task=args.attempts,
        timeout_s=args.timeout,
        parallel_models=args.parallel_models,
    )
    if args.report_out:
        out_path = Path(args.report_out)
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(render_markdown_report(summary))
    print(json.dumps({"run_dir": str(Path(args.run_dir).resolve()), "models": len(models), "tasks": len(tasks)}))


def cmd_manual(args):
    cases_path = Path(args.cases) if args.cases else HERE / "tasks" / "cases-r.json"
    cases = json.loads(cases_path.read_text())
    out = run_manual_cases(Path(args.run_dir), cases)
    print(json.dumps({"cases": len(out.get("cases", [])), "gaps": out.get("db_summary", {}).get("native_gaps", [])}))


def cmd_gaps(args):
    res = analyze_store_db(Path(args.db), args.sessions or None)
    print(json.dumps(res, indent=2))


def cmd_summarize(args):
    for p in args.files:
        for item in json.loads(Path(p).read_text()):
            raw = item.get("tool_result") or item.get("error") or ""
            body = raw.split("⟦D:host:tool_feedback⟧")[0].replace("⟦D⟧", "").replace("⟦D:tool⟧", "").strip()
            codes = OAR_CODE_RE.findall(raw)
            m = CMD_JSON_RE.search(body)
            print("=====", item.get("label"), "|", item.get("command", "").replace("\n", "\\n"), "| t=", item.get("elapsed_s"))
            if m:
                obj = json.loads(m.group(0))
                print(
                    f"state={obj.get('state')} status={obj.get('status')} "
                    f"stop={obj.get('stop_code')} children={obj.get('executed_children')}"
                )
                print("STDOUT:", scrub_text(obj.get("stdout", "")[:600]).replace("\n", "\\n"))
                print("STDERR:", scrub_text(obj.get("stderr", "")[:600]).replace("\n", "\\n"))
            else:
                print(scrub_text(body[:800]), "codes:", codes[:3])


def cmd_tx(args):
    data = json.loads(Path(args.messages_file).read_text())
    for msg in data.get("messages", []):
        role = msg.get("role")
        if role == "system":
            continue
        content = (msg.get("tool_result") or {}).get("content") or msg.get("content") or ""
        if role == "tool":
            content = content.split("⟦D:host:tool_feedback⟧")[0].replace("⟦D⟧", "").replace("⟦D:tool⟧", "").strip()
        print("---", role)
        if content:
            print(scrub_text(content[: args.limit]))
        for tc in msg.get("tool_calls") or []:
            print("CALL", tc.get("name"), scrub_text(json.dumps(tc.get("args"))[:600]))


def cmd_llmtx(args):
    bodies = {}
    last = None
    reqs = 0
    for line in Path(args.jsonl_file).read_text().splitlines():
        if not line.strip():
            continue
        d = json.loads(line)
        for m in d.get("messages") or []:
            c = m.get("content") or ""
            mm = re.search(r"\n?\[capture-body ([A-Za-z0-9_-]+)\]$", c)
            if mm:
                bodies[mm.group(1)] = c[: mm.start()]
        if d.get("session_id") == args.session_id:
            last = d
            reqs += 1
    if not last:
        print("session not found")
        return
    print("requests:", reqs, "model:", last.get("model"))
    for m in last.get("messages") or []:
        role = m.get("role")
        if role == "system":
            continue
        c = m.get("content") or ""
        mm = re.match(r"\[capture-body ([A-Za-z0-9_-]+) —", c)
        if mm:
            c = bodies.get(mm.group(1), c)
        c = re.sub(r"\n?\[capture-body [A-Za-z0-9_-]+\]$", "", c)
        print("---", role)
        if c:
            print(scrub_text(c[: args.limit]))


def cmd_engine(args):
    eng = IsolatedEngine(Path(args.run_dir), args.name)
    if args.action == "start":
        provider_id, model = args.model.split(":", 1) if ":" in args.model else ("together-ai-1", args.model)
        print(json.dumps(eng.start(mode=args.mode, provider_id=provider_id, model=model)))
    elif args.action == "stop":
        print(json.dumps({"stopped": eng.stop()}))
    elif args.action == "setup":
        print(json.dumps(eng.setup_session(Path(args.project))))
    elif args.action == "prompt":
        print(json.dumps(eng.run_prompt(args.label, args.text, approve=args.approve, timeout=args.timeout)))
    elif args.action == "dump":
        print(json.dumps(eng.dump_case(args.label)))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="subcommand", required=True)

    p_suite = sub.add_parser("suite", help="Run the multi-model live task suite")
    p_suite.add_argument("--run-dir", required=True, help="Directory for isolated run state and outputs")
    p_suite.add_argument("--models", default="", help="Comma-separated provider_id:model list")
    p_suite.add_argument("--tasks", default="", help="Path to live-tasks.json")
    p_suite.add_argument("--only", default="", help="Comma-separated task label prefixes")
    p_suite.add_argument("--attempts", type=int, default=1, help="Attempts per task per model")
    p_suite.add_argument("--timeout", type=float, default=150.0, help="Per-prompt timeout in seconds")
    p_suite.add_argument("--parallel-models", type=int, default=3, help="Max models to run concurrently")
    p_suite.add_argument("--report-out", default="", help="Optional path to copy screened Markdown report")
    p_suite.set_defaults(func=cmd_suite)

    p_man = sub.add_parser("manual", help="Run deterministic manual command cases")
    p_man.add_argument("--run-dir", required=True, help="Directory for isolated run state")
    p_man.add_argument("--cases", default="", help="Path to cases JSON file")
    p_man.set_defaults(func=cmd_manual)

    p_gaps = sub.add_parser("gaps", help="Tally native_gap facts and refusals from store.db")
    p_gaps.add_argument("--db", required=True, help="Path to store.db")
    p_gaps.add_argument("sessions", nargs="*", help="Optional session IDs to filter")
    p_gaps.set_defaults(func=cmd_gaps)

    p_sum = sub.add_parser("summarize", help="Summarize manual case output JSON files")
    p_sum.add_argument("files", nargs="+", help="Manual output JSON files")
    p_sum.set_defaults(func=cmd_summarize)

    p_tx = sub.add_parser("tx", help="Print compact transcript from messages.json")
    p_tx.add_argument("messages_file", help="Path to messages.json")
    p_tx.add_argument("--limit", type=int, default=700)
    p_tx.set_defaults(func=cmd_tx)

    p_ltx = sub.add_parser("llmtx", help="Print final conversation from llm-requests.jsonl")
    p_ltx.add_argument("jsonl_file", help="Path to llm-requests.jsonl")
    p_ltx.add_argument("session_id", help="Session ID")
    p_ltx.add_argument("--limit", type=int, default=900)
    p_ltx.set_defaults(func=cmd_llmtx)

    p_eng = sub.add_parser("engine", help="Low-level isolated engine control")
    p_eng.add_argument("action", choices=("start", "stop", "setup", "prompt", "dump"))
    p_eng.add_argument("--run-dir", required=True)
    p_eng.add_argument("--name", default="dlive")
    p_eng.add_argument("--mode", choices=("live", "manual"), default="live")
    p_eng.add_argument("--model", default="together-ai-1:zai-org/GLM-5.3-Flash")
    p_eng.add_argument("--project", default="")
    p_eng.add_argument("--label", default="case")
    p_eng.add_argument("--text", default="")
    p_eng.add_argument("--approve", default="reject")
    p_eng.add_argument("--timeout", type=float, default=240.0)
    p_eng.set_defaults(func=cmd_engine)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    args.func(args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
