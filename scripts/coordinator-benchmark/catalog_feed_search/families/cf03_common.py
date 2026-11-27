"""Shared sidecar lifecycle, verdict accounting, shrinking, and evidence helpers for CF03."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass, field
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import time
from typing import Any, Callable
import urllib.error
import urllib.request

from ..checker import ProductCatalogAnswer, check_catalog, walk_entries_to_catalog_items
from ..checker_types import CheckResult, Verdict
from ..driver_adapters import map_catalog_index_matches_to_answer
from ..driver_http import HTTPTransport
from ..driver_provenance import RunProvenance, file_sha256, write_evidence_manifest
from ..driver_sidecar import SidecarDriver
from ..generators import BoundaryConfig, TreeSpec, materialize_tree
from ..references import reference_walk
from ..seeds import ReplaySelector
from ..shrinker import FailingInstance, ShrinkResult, shrink_instance


def pick_free_port() -> int:
    """Bind an ephemeral loopback port and return it immediately."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class ManagedSidecar:
    """Isolated lycaon-dev sidecar process with scratch config, ephemeral port, and PID cleanup."""

    def __init__(
        self,
        binary_path: Path,
        config_dir: Path,
        *,
        repo_root: Path | None = None,
        token: str | None = None,
        startup_timeout_s: float = 45.0,
    ) -> None:
        self.binary_path = Path(binary_path).resolve()
        self.config_dir = Path(config_dir).resolve()
        self.repo_root = Path(repo_root).resolve() if repo_root is not None else None
        self.token = token or secrets.token_hex(24)
        self.startup_timeout_s = startup_timeout_s
        self.proc: subprocess.Popen[bytes] | None = None
        self.port: int = 0
        self.url: str = ""
        self.started_utc: str = ""
        self._log_file = None

    def start(self) -> dict[str, object]:
        """Start lycaon-dev serve on an ephemeral port and wait until /health returns 200."""
        self.config_dir.mkdir(parents=True, exist_ok=True)
        self.port = pick_free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        env = dict(os.environ)
        env.update(
            {
                "LYCAON_CONFIG_DIR": str(self.config_dir),
                "LYCAON_ADDR": f"127.0.0.1:{self.port}",
                "LYCAON_API_TOKEN": self.token,
                "LYCAON_DEV": "1",
                "LYCAON_LLM_MOCK": "1",
            }
        )
        config_root_env = env.get("LYCAON_CONFIG_ROOT", "").strip()
        if config_root_env:
            run_cwd: str | None = str(Path(config_root_env).resolve().parent)
        elif self.repo_root is not None:
            env["LYCAON_CONFIG_ROOT"] = str(self.repo_root / "lycaon")
            run_cwd = str(self.repo_root)
        else:
            run_cwd = None
        log_path = self.config_dir / "sidecar.log"
        self._log_file = open(log_path, "ab")
        self.proc = subprocess.Popen(
            [str(self.binary_path), "serve", "--db", str(self.config_dir / "store.db")],
            cwd=run_cwd,
            env=env,
            stdout=self._log_file,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        self.started_utc = datetime.now(timezone.utc).isoformat()
        self._wait_healthy()
        return {
            "utc_started": self.started_utc,
            "pid": self.proc.pid,
            "url": self.url,
            "binary_path": str(self.binary_path),
            "binary_sha256": file_sha256(self.binary_path),
        }

    def _wait_healthy(self) -> None:
        deadline = time.monotonic() + self.startup_timeout_s
        health_url = f"{self.url}/health"
        while time.monotonic() < deadline:
            if self.proc is not None and self.proc.poll() is not None:
                log_tail = ""
                try:
                    log_tail = (self.config_dir / "sidecar.log").read_text(encoding="utf-8", errors="replace")[-1000:]
                except OSError:
                    pass
                raise RuntimeError(f"lycaon-dev exited early with code {self.proc.returncode}: {log_tail}")
            try:
                req = urllib.request.Request(health_url, method="GET")
                with urllib.request.urlopen(req, timeout=1.0) as resp:
                    if resp.status == 200:
                        return
            except (urllib.error.URLError, TimeoutError, OSError):
                time.sleep(0.1)
        self.stop()
        raise TimeoutError(f"lycaon-dev did not become healthy at {health_url} within {self.startup_timeout_s}s")

    def driver(self) -> SidecarDriver:
        return SidecarDriver(HTTPTransport(self.url, self.token))

    def stop(self) -> None:
        """Stop ONLY the exact PID we launched."""
        if self.proc is not None:
            if self.proc.poll() is None:
                try:
                    self.proc.send_signal(signal.SIGTERM)
                    self.proc.wait(timeout=5.0)
                except (ProcessLookupError, subprocess.TimeoutExpired):
                    try:
                        self.proc.kill()
                        self.proc.wait(timeout=3.0)
                    except (ProcessLookupError, subprocess.TimeoutExpired):
                        pass
            self.proc = None
        if self._log_file is not None:
            try:
                self._log_file.close()
            except OSError:
                pass
            self._log_file = None

    def restart(self) -> dict[str, object]:
        """Stop and restart lycaon-dev against the exact same persisted LYCAON_CONFIG_DIR."""
        self.stop()
        return self.start()

    def __enter__(self) -> "ManagedSidecar":
        self.start()
        return self

    def __exit__(self, *_: object) -> None:
        self.stop()


@dataclass
class FamilyReport:
    """Per-family verdict counts, declared-gap rates, and retained failures."""

    family: str
    lane: str
    base_seed: str
    planned: int
    edge_planned: int
    executed: int = 0
    passed: int = 0
    passed_clean: int = 0
    passed_declared_gap: int = 0
    failed: int = 0
    blocked: int = 0
    inconclusive: int = 0
    edge_executed: int = 0
    edge_passed: int = 0
    edge_failed: int = 0
    edge_blocked: int = 0
    declared_gaps: Counter[str] = field(default_factory=Counter)
    failures: list[dict[str, object]] = field(default_factory=list)
    shrunk_files: list[str] = field(default_factory=list)
    edge_results: list[dict[str, object]] = field(default_factory=list)
    state_ledger: list[dict[str, object]] = field(default_factory=list)
    notes: list[str] = field(default_factory=list)

    def record_generated(self, check: CheckResult, *, extra: dict[str, object] | None = None) -> None:
        self.executed += 1
        if check.verdict == Verdict.PASS:
            self.passed += 1
            self.passed_clean += 1
        elif check.verdict == Verdict.PASS_DECLARED_GAP:
            self.passed += 1
            self.passed_declared_gap += 1
            for g in check.accounted_gaps:
                self.declared_gaps[g] += 1
        elif check.verdict == Verdict.DEFECT:
            self.failed += 1
            rec: dict[str, object] = {
                "verdict": check.verdict.value,
                "reason": check.reason,
                "extra_claims": list(check.extra_claims[:20]),
                "missing_claims": list(check.missing_claims[:20]),
                "wrong_claims": list(check.wrong_claims[:20]),
                "declared_gaps": list(check.declared_gaps),
            }
            if extra:
                rec.update(extra)
            self.failures.append(rec)
        else:
            self.inconclusive += 1

    def record_blocked(self, reason: str, *, extra: dict[str, object] | None = None) -> None:
        self.executed += 1
        self.blocked += 1
        rec: dict[str, object] = {"status": "blocked", "reason": reason}
        if extra:
            rec.update(extra)
        self.state_ledger.append(rec)

    def record_ledger(self, entry: dict[str, object]) -> None:
        self.state_ledger.append(entry)

    def record_edge(
        self,
        name: str,
        passed: bool,
        detail: dict[str, object],
        gap: str | None = None,
        *,
        blocked: bool = False,
    ) -> None:
        self.edge_executed += 1
        if blocked:
            self.edge_blocked += 1
            self.edge_results.append({"edge": name, "passed": False, "status": "blocked", "detail": detail})
            return
        if passed:
            self.edge_passed += 1
            if gap:
                self.declared_gaps[gap] += 1
        else:
            self.edge_failed += 1
        self.edge_results.append(
            {"edge": name, "passed": passed, "status": "passed" if passed else "failed", "detail": detail}
        )

    @property
    def declared_gap_rate(self) -> float:
        total = self.executed + self.edge_executed
        if total == 0:
            return 0.0
        gap_total = sum(self.declared_gaps.values())
        return round(gap_total / total, 6)

    def to_dict(self) -> dict[str, object]:
        return {
            "family": self.family,
            "lane": self.lane,
            "base_seed": self.base_seed,
            "planned": self.planned,
            "executed": self.executed,
            "passed": self.passed,
            "passed_clean": self.passed_clean,
            "passed_declared_gap": self.passed_declared_gap,
            "failed": self.failed,
            "blocked": self.blocked,
            "inconclusive": self.inconclusive,
            "edge_planned": self.edge_planned,
            "edge_executed": self.edge_executed,
            "edge_passed": self.edge_passed,
            "edge_failed": self.edge_failed,
            "edge_blocked": self.edge_blocked,
            "declared_gaps": dict(sorted(self.declared_gaps.items())),
            "declared_gap_rate": self.declared_gap_rate,
            "failures": self.failures,
            "shrunk_files": self.shrunk_files,
            "edge_results": self.edge_results,
            "state_ledger": self.state_ledger,
            "notes": self.notes,
        }


def verify_dir_unreadable(path: Path) -> bool:
    """Independently verify that reading directory entries raises PermissionError."""
    try:
        os.listdir(path)
    except PermissionError:
        return True
    except OSError:
        return False
    return False


def walk_combined_catalog(
    driver: SidecarDriver,
    project_id: str,
    *,
    page_limit: int = 200,
) -> tuple[ProductCatalogAnswer, list[dict[str, object]]]:
    """Query /source/search across ALL project roots (root_id omitted) in one combined selection."""
    all_matches: list[dict[str, object]] = []
    cursor: str | None = None
    last_coverage: list[dict[str, object]] = []
    revision: int | None = None

    while True:
        resp = driver.file_lookup(project_id, "", root_id=None, limit=page_limit, cursor=cursor)
        if resp.get("revision") is not None:
            revision = int(resp["revision"])
        cov = resp.get("coverage")
        if isinstance(cov, list):
            last_coverage = [c for c in cov if isinstance(c, dict)]
        matches = resp.get("matches") or []
        if isinstance(matches, list):
            all_matches.extend(m for m in matches if isinstance(m, dict))
        next_cur = str(resp.get("next_cursor") or "")
        if not next_cur or next_cur == cursor:
            break
        cursor = next_cur

    answer = map_catalog_index_matches_to_answer(
        all_matches,
        last_coverage,
        generation_revision=revision,
    )
    return answer, last_coverage


def browse_entries_map(driver: SidecarDriver, project_id: str, root_id: str, dir_path: str = ".") -> dict[str, bool]:
    """Return {entry_name: is_dir} from /source/browse for direct type, empty-dir, and boundary checks."""
    resp = driver.browse_dir(project_id, root_id, dir_path)
    out: dict[str, bool] = {}
    for entry in resp.get("entries") or []:
        if isinstance(entry, dict) and entry.get("name"):
            out[str(entry["name"])] = bool(entry.get("is_dir"))
    return out


def parse_http_error_json(exc: urllib.error.HTTPError) -> dict[str, object]:
    """Safely decode JSON error body from an urllib HTTPError."""
    try:
        raw = exc.read()
        if isinstance(raw, bytes) and raw:
            parsed = json.loads(raw.decode("utf-8", errors="replace"))
            if isinstance(parsed, dict):
                return parsed
    except Exception:
        pass
    return {}


def walk_catalog_retry_409(
    driver: SidecarDriver,
    project_id: str,
    root_id: str,
    *,
    page_limit: int = 200,
    max_attempts: int = 3,
) -> ProductCatalogAnswer:
    """Walk settled catalog index, retrying from page 1 if a background refresh triggers HTTP 409."""
    for attempt in range(max_attempts):
        try:
            return driver.walk_catalog_index(project_id, root_id, page_limit=page_limit)
        except urllib.error.HTTPError as exc:
            if exc.code == 409 and attempt + 1 < max_attempts:
                continue
            raise
    return driver.walk_catalog_index(project_id, root_id, page_limit=page_limit)


def shrink_catalog_failure(
    selector: ReplaySelector,
    spec: TreeSpec,
    failure_summary: str,
    eval_fn: Callable[[TreeSpec], CheckResult],
    out_dir: Path,
    *,
    max_evaluations: int = 80,
) -> ShrinkResult | None:
    """Shrink a failing catalog TreeSpec using shrinker.py and save JSON under out_dir/shrunk/."""
    failing = FailingInstance(selector=selector, tree_spec=spec, failure_summary=failure_summary)

    def _predicate(cand: FailingInstance) -> bool:
        if not cand.tree_spec.entries:
            return False
        res = eval_fn(cand.tree_spec)
        return res.verdict == Verdict.DEFECT

    if not _predicate(failing):
        return None
    result = shrink_instance(failing, _predicate, max_evaluations=max_evaluations)
    shrunk_dir = Path(out_dir) / "shrunk"
    shrunk_dir.mkdir(parents=True, exist_ok=True)
    target = shrunk_dir / f"{selector.family_id}_{selector.base_seed}_{selector.instance_index}.json"
    result.write_json(target)
    return result


def write_sha256sums(directory: Path) -> Path:
    """Write standard SHA256SUMS.txt covering every file under directory."""
    root = Path(directory).resolve()
    lines: list[str] = []
    for path in sorted(p for p in root.rglob("*") if p.is_file() and p.name != "SHA256SUMS.txt"):
        rel = path.relative_to(root).as_posix()
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        lines.append(f"{digest}  {rel}")
    out_path = root / "SHA256SUMS.txt"
    out_path.write_text("\n".join(lines) + ("\n" if lines else ""), encoding="utf-8")
    return out_path


def evaluate_tree_against_sidecar(
    driver: SidecarDriver,
    spec: TreeSpec,
    work_root: Path,
    boundary_config: BoundaryConfig,
    *,
    settle_wait_s: float = 15.0,
) -> tuple[CheckResult, ProductCatalogAnswer, dict[str, object]]:
    """Materialize spec, attach project, wait for catalog settlement, and run D4 check_catalog."""
    from ..live_controls import wait_catalog_settled

    materialize_tree(spec, work_root)
    project_id, root_ids = driver.attach_project([work_root])
    root_id = root_ids[0] if root_ids else "root-0"
    settled, polls, summary = wait_catalog_settled(driver, project_id, root_id, max_wait_s=settle_wait_s)
    ref_entries = [
        e
        for e in reference_walk(
            work_root,
            boundary_config=boundary_config,
            include_hidden=True,
            prune_nested_checkouts=True,
        )
        if e.kind == "file"
    ]
    ref_items = walk_entries_to_catalog_items(ref_entries, root_id=root_id)
    answer = driver.walk_catalog_index(project_id, root_id)
    res = check_catalog(ref_items, answer)
    meta: dict[str, object] = {
        "project_id": project_id,
        "root_id": root_id,
        "settled": settled,
        "settle_polls": polls,
        "reference_count": len(ref_items),
        "product_count": len(answer.items),
        "raw_index_summary": summary,
    }
    return res, answer, meta
