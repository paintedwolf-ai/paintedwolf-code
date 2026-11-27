"""CF07 G03 · Budgets and refusals (sidecar lane + G03-INC-01 & G03-INC-02)."""

from __future__ import annotations

from collections import Counter
import os
from pathlib import Path
import shlex
import tempfile

from ..live_controls import wait_catalog_settled
from ..seeds import seed_rng
from .cf07_harness import AgentToolSession, CapturedToolCall, D1SidecarProcess

UNSUPPORTED_PCRE_PATTERNS = (
    r"(?R)",
    r"(?<=a+)b",
    r"(?<!foo+)bar",
    r"(?(1)a|b)",
    r"(?0)",
    r"(?P>sub)",
)


def evaluate_g03_refusal(
    call: CapturedToolCall,
    *,
    expected_code: str,
    allow_partial_stdout: bool = False,
) -> dict[str, object]:
    """Evaluate a G03 ceiling call against its expected typed refusal code."""
    code_ok = call.rejection_code == expected_code
    stdout_ok = allow_partial_stdout or (call.stdout.strip() == "")
    verdict = "PASS" if (code_ok and stdout_ok) else "DEFECT"
    issues: list[str] = []
    if not code_ok:
        issues.append(f"expected rejection_code={expected_code}, got {call.rejection_code!r} (outcome={call.outcome})")
    if not stdout_ok:
        issues.append(f"unexpected stdout on pre-scan refusal: {call.stdout[:160]!r}")
    return {
        "verdict": verdict,
        "expected_code": expected_code,
        "observed_code": call.rejection_code,
        "outcome": call.outcome,
        "status_code": call.status_code,
        "stop_code": call.stop_code,
        "has_partial_stdout": bool(call.stdout.strip()),
        "declared_gaps": list(call.declared_gaps),
        "issues": issues,
    }


def run_g03_sidecar(
    sidecar: D1SidecarProcess,
    *,
    base_seed: str,
    instances: int = 40,
) -> dict[str, object]:
    """Run G03 sidecar instances (40) + 2 edges + G03-INC-01 & G03-INC-02 incident replays."""
    assert sidecar.driver is not None
    verdict_counts: Counter[str] = Counter()
    gap_counts: Counter[str] = Counter()
    gap_samples: list[dict[str, object]] = []
    defects: list[dict[str, object]] = []

    with tempfile.TemporaryDirectory(prefix="cf07-g03-") as tmp:
        tmp_path = Path(tmp)
        repo_dir = tmp_path / "g03_repo"
        repo_dir.mkdir(parents=True, exist_ok=True)
        (repo_dir / "input.txt").write_text("alpha beta\n" + ("a" * 30) + "!\n", encoding="utf-8")
        (repo_dir / "01_first.txt").write_text("aaab\nalpha first\n", encoding="utf-8")
        (repo_dir / "02_budget.txt").write_text(("a" * 30) + "!\n", encoding="utf-8")

        pid, rids = sidecar.driver.attach_project([repo_dir])
        wait_catalog_settled(sidecar.driver, pid, rids[0], max_wait_s=15.0)
        # Keep G03 refusal batches small (4 calls/session) so consecutive-rejection
        # turn guards never halt the session mid-batch.
        batch_size = 4
        for batch_start in range(0, instances, batch_size):
            batch_end = min(instances, batch_start + batch_size)
            session = AgentToolSession(sidecar, pid)
            try:
                for idx in range(batch_start, batch_end):
                    rng = seed_rng(base_seed, "G03", idx)
                    tool = "rg" if rng.randint(0, 1) == 1 else "grep"
                    if idx % 2 == 0:
                        pat = UNSUPPORTED_PCRE_PATTERNS[rng.randint(0, len(UNSUPPORTED_PCRE_PATTERNS) - 1)]
                        cmd = f"{tool} -P {shlex.quote(pat)} input.txt"
                        expected_code = "GREP_REGEX_UNSUPPORTED"
                        allow_partial = False
                    else:
                        if rng.randint(0, 3) == 0:
                            cmd = f"{tool} -P {shlex.quote('(a+)+b')} input.txt"
                            allow_partial = True
                        else:
                            pat = "a" * (520 + rng.randint(0, 180))
                            cmd = f"{tool} {shlex.quote(pat)} input.txt"
                            allow_partial = False
                        expected_code = "GREP_MATCH_BUDGET"

                    call = session.invoke_tool("command", {"command": cmd})
                    ev = evaluate_g03_refusal(call, expected_code=expected_code, allow_partial_stdout=allow_partial)
                    verdict_counts[str(ev["verdict"])] += 1
                    for g in call.declared_gaps:
                        gap_counts[g] += 1
                    if call.declared_gaps and len(gap_samples) < 10:
                        gap_samples.append({"instance": idx, "command": cmd, "call": call.to_dict()})
                    if ev["verdict"] == "DEFECT" and len(defects) < 10:
                        defects.append({"instance": idx, "command": cmd, "evaluation": ev, "call": call.to_dict()})
            finally:
                session.close()

        session = AgentToolSession(sidecar, pid)
        try:

            # 2 hand-written edges
            edge_specs = [
                ("G03-edge-unsupported-pcre", "rg -P '(?R)' input.txt", "GREP_REGEX_UNSUPPORTED", False),
                ("G03-edge-pattern-bytes-budget", f"grep {shlex.quote('x' * 600)} input.txt", "GREP_MATCH_BUDGET", False),
            ]
            edge_results: list[dict[str, object]] = []
            for name, cmd, exp_code, allow_p in edge_specs:
                call = session.invoke_tool("command", {"command": cmd})
                ev = evaluate_g03_refusal(call, expected_code=exp_code, allow_partial_stdout=allow_p)
                edge_results.append({"edge": name, "command": cmd, "evaluation": ev, "captured_call": call.to_dict()})

            # Incident edge slot G03-INC-01: deterministic work-budget fixture hitting regex_work_steps
            # after emitting a partial match from 01_first.txt.
            inc01_cmd = "rg -P '(a+)+b' 01_first.txt 02_budget.txt"
            inc01_call = session.invoke_tool("command", {"command": inc01_cmd})
            inc01_ev = evaluate_g03_refusal(inc01_call, expected_code="GREP_MATCH_BUDGET", allow_partial_stdout=True)
            inc01_replay = {
                "slot": "G03-INC-01",
                "command": inc01_cmd,
                "reproduced": inc01_call.rejection_code == "GREP_MATCH_BUDGET",
                "regex_work_steps_mentioned": "regex_work_steps" in (inc01_call.stderr + inc01_call.model_facing_message),
                "partial_stdout_emitted": "aaab" in inc01_call.stdout,
                "evaluation": inc01_ev,
                "captured_call": inc01_call.to_dict(),
            }

            # Incident edge slot G03-INC-02: partial stdout + unreadable/missing target -> exit 2, empty stop_code.
            unreadable = repo_dir / "unreadable_secret.txt"
            unreadable.write_text("alpha unreadable\n", encoding="utf-8")
            os.chmod(unreadable, 0o000)
            try:
                inc02_rg_cmd = "rg alpha 01_first.txt unreadable_secret.txt"
                inc02_rg_call = session.invoke_tool("command", {"command": inc02_rg_cmd})
                inc02_grep_cmd = "grep alpha 01_first.txt missing_target.txt"
                inc02_grep_call = session.invoke_tool("command", {"command": inc02_grep_cmd})
            finally:
                os.chmod(unreadable, 0o644)

            inc02_replay = {
                "slot": "G03-INC-02",
                "rg_probe": {
                    "command": inc02_rg_cmd,
                    "partial_stdout_emitted": "alpha first" in inc02_rg_call.stdout,
                    "status_code": inc02_rg_call.status_code,
                    "stop_code": inc02_rg_call.stop_code,
                    "outcome": inc02_rg_call.outcome,
                    "stderr_non_empty": bool(inc02_rg_call.stderr.strip()),
                    "captured_call": inc02_rg_call.to_dict(),
                },
                "grep_probe": {
                    "command": inc02_grep_cmd,
                    "partial_stdout_emitted": "alpha first" in inc02_grep_call.stdout,
                    "status_code": inc02_grep_call.status_code,
                    "stop_code": inc02_grep_call.stop_code,
                    "outcome": inc02_grep_call.outcome,
                    "stderr_non_empty": bool(inc02_grep_call.stderr.strip()),
                    "captured_call": inc02_grep_call.to_dict(),
                },
                "reproduced": (
                    ("alpha first" in inc02_grep_call.stdout)
                    and (inc02_grep_call.status_code == 2 or "status_code\":2" in inc02_grep_call.model_facing_message or inc02_grep_call.outcome != "completed")
                ),
            }
        finally:
            session.close()

    return {
        "family": "G03",
        "lane": "sidecar",
        "base_seed": base_seed,
        "planned_instances": instances,
        "executed_instances": sum(verdict_counts.values()),
        "verdict_counts": dict(verdict_counts),
        "declared_gap_counts": dict(gap_counts),
        "declared_gap_samples": gap_samples,
        "defects": defects,
        "edges": edge_results,
        "incident_replays": {
            "G03-INC-01": inc01_replay,
            "G03-INC-02": inc02_replay,
        },
    }
