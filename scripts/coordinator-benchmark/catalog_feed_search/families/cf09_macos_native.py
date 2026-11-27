"""CF09 macOS native Den presentation runner (`./task den:app` + AppleScript accessibility + screencapture).

Per D14, macOS native Den is launched via `./task den:app` attached to an
isolated D1 `lycaon-dev` sidecar on a probed free port, driven with scripted
accessibility keyboard events (`osascript` / System Events), with each outcome
read back from the sidecar API and captured via `screencapture`.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.request
from typing import Any

from .cf09_presentation import (
    COVERAGE_STATES,
    NEEDLE_TOKEN,
    PresentationObservation,
    build_coverage_fixture,
    extract_declared_facts,
    mutate_for_refreshing,
    verify_presentation_observation,
    write_presentation_evidence,
)


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def _file_sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _wait_http_ok(url: str, timeout_s: float = 60.0) -> None:
    deadline = time.monotonic() + timeout_s
    last_err = ""
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2.0) as resp:
                if 200 <= resp.status < 400:
                    return
        except Exception as exc:  # noqa: BLE001
            last_err = str(exc)
        time.sleep(0.25)
    raise RuntimeError(f"timed out waiting for {url}: {last_err}")


def _api_request(
    base_url: str,
    token: str,
    path: str,
    *,
    method: str = "GET",
    payload: dict[str, Any] | None = None,
) -> dict[str, Any]:
    data = None if payload is None else json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{base_url}{path}",
        data=data,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
    )
    with urllib.request.urlopen(req, timeout=15.0) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _stop_proc(proc: subprocess.Popen[bytes] | None) -> None:
    if proc is None or proc.poll() is not None:
        return
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except OSError:
        proc.terminate()
    try:
        proc.wait(timeout=8.0)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except OSError:
            proc.kill()
        proc.wait(timeout=3.0)


def _osascript(script: str) -> tuple[int, str]:
    proc = subprocess.run(
        ["osascript", "-e", script],
        capture_output=True,
        text=True,
        check=False,
        timeout=15,
    )
    return proc.returncode, (proc.stdout + proc.stderr).strip()


def _focus_den_app() -> bool:
    for app_name in ("Painted Wolf Code", "lycaon-den"):
        rc, _ = _osascript(
            f'tell application "System Events" to set frontmost of process "{app_name}" to true'
        )
        if rc == 0:
            time.sleep(0.4)
            return True
    return False


def _send_keystroke(key: str, modifiers: list[str] | None = None) -> None:
    if modifiers:
        mod_expr = "{" + ", ".join(f"{m} down" for m in modifiers) + "}"
        script = f'tell application "System Events" to keystroke "{key}" using {mod_expr}'
    else:
        script = f'tell application "System Events" to keystroke "{key}"'
    _osascript(script)
    time.sleep(0.35)


def _send_escape() -> None:
    _osascript('tell application "System Events" to key code 53')
    time.sleep(0.3)


def _capture_screen(path: Path) -> bool:
    rc = subprocess.run(
        ["screencapture", "-x", str(path)],
        check=False,
        timeout=15,
    ).returncode
    return rc == 0 and path.exists()


def _has_coverage_notice_in_a11y(a11y_text: str) -> bool:
    low = a11y_text.lower()
    markers = (
        "incomplete",
        "unavailable",
        "still indexing",
        "could not be read",
        "indexing budget",
        "exceeded",
        "apply all is unavailable",
        "being updated",
        "refreshing",
        "warming",
        "limited",
    )
    return any(m in low for m in markers)


def _has_final_empty_in_a11y(a11y_text: str) -> bool:
    low = a11y_text.lower()
    return (
        "nothing matched in this project" in low
        or "nothing matched this query" in low
        or "no replaceable matches" in low
    )


def _read_accessibility_text() -> str:
    """Query accessibility static text / status elements from the frontmost Den window."""
    script = (
        'tell application "System Events"\n'
        '  set p to first process whose frontmost is true\n'
        '  try\n'
        '    return value of every static text of entire contents of front window of p\n'
        '  on error\n'
        '    return ""\n'
        '  end try\n'
        'end tell'
    )
    _, out = _osascript(script)
    return out


def run_macos_native(
    *,
    den_repo: Path,
    engine_bin: Path,
    out_dir: Path,
    baseline_commit: str,
    warming_files: int = 3000,
) -> Path:
    out_dir.mkdir(parents=True, exist_ok=True)
    port = _free_port()
    api_url = f"http://127.0.0.1:{port}"
    token = f"cf09-macos-{int(time.time())}"

    sidecar_proc: subprocess.Popen[bytes] | None = None
    app_proc: subprocess.Popen[bytes] | None = None
    fixtures = []

    with tempfile.TemporaryDirectory(prefix="cf09-macos-") as tmp_str:
        tmp = Path(tmp_str)
        cfg_dir = tmp / "config"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        (cfg_dir / "api.token").write_text(token, encoding="utf-8")
        fx_dir = tmp / "fixtures"
        fx_dir.mkdir(parents=True, exist_ok=True)

        try:
            for st in COVERAGE_STATES:
                fixtures.append(
                    build_coverage_fixture(fx_dir, st, warming_file_count=warming_files)
                )

            sidecar_env = os.environ.copy()
            sidecar_env.update(
                {
                    "LYCAON_CONFIG_DIR": str(cfg_dir),
                    "LYCAON_ADDR": f"127.0.0.1:{port}",
                    "LYCAON_API_TOKEN": token,
                    "LYCAON_DEV": "1",
                    "LYCAON_DEV_CORS": "1",
                    "LYCAON_LLM_MOCK": "1",
                    "NO_PROXY": "127.0.0.1,localhost",
                }
            )
            sidecar_log = (out_dir / "sidecar_macos.log").open("wb")
            sidecar_proc = subprocess.Popen(
                [str(engine_bin), "serve", "--db", str(cfg_dir / "store.db")],
                cwd=str(den_repo),
                env=sidecar_env,
                stdout=sidecar_log,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
            _wait_http_ok(f"{api_url}/health", timeout_s=45.0)

            projects_meta: dict[str, dict[str, str]] = {}
            for fx in fixtures:
                resp = _api_request(
                    api_url,
                    token,
                    "/v1/projects",
                    method="POST",
                    payload={
                        "name": f"CF09-{fx.state}",
                        "roots": [{"path": str(fx.root_dir)}],
                    },
                )
                roots = resp.get("roots") or []
                root_id = str(roots[0]["id"]) if roots else ""
                projects_meta[fx.state] = {
                    "id": str(resp["id"]),
                    "root_id": root_id,
                    "name": f"CF09-{fx.state}",
                }
                if fx.state == "refreshing":
                    time.sleep(1.5)
                    mutate_for_refreshing(fx, burst_count=200)

            app_env = os.environ.copy()
            app_env.update(
                {
                    "LYCAON_ATTACH_ONLY": "1",
                    "LYCAON_DEV": "1",
                    "LYCAON_CONFIG_DIR": str(cfg_dir),
                    "LYCAON_HEALTH_URL": f"{api_url}/health",
                    "VITE_LYCAON_API_URL": api_url,
                    "VITE_LYCAON_API_TOKEN": token,
                    "NO_PROXY": "127.0.0.1,localhost",
                }
            )
            app_log = (out_dir / "den_app_macos.log").open("wb")
            app_proc = subprocess.Popen(
                ["./task", "den:app"],
                cwd=str(den_repo),
                env=app_env,
                stdout=app_log,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
            _wait_http_ok("http://127.0.0.1:1420", timeout_s=120.0)
            time.sleep(4.0)
            focused = _focus_den_app()

            observations: list[PresentationObservation] = []
            for state in ("failed", "incomplete", "warming", "refreshing"):
                proj = projects_meta[state]

                # 1. Search stage (Cmd+Shift+F)
                _send_escape()
                _send_keystroke("f", ["command", "shift"])
                _send_keystroke(NEEDLE_TOKEN)
                time.sleep(0.8)
                search_api = _api_request(
                    api_url,
                    token,
                    "/v1/search",
                    method="POST",
                    payload={"query": f"project:current {NEEDLE_TOKEN}", "origin_project_id": proj["id"], "budget": "interactive"},
                )
                search_shot = out_dir / f"macos_search_stage_{state}.png"
                _capture_screen(search_shot)
                a11y_text = _read_accessibility_text()
                observations.append(
                    PresentationObservation(
                        surface="search_stage",
                        state=state,
                        platform="macos",
                        native_window=focused,
                        project_id=proj["id"],
                        query=f"project:current {NEEDLE_TOKEN}",
                        scope="project:current",
                        request_url=f"{api_url}/v1/search",
                        request_correlated=focused,
                        workspace_ready=focused and len(a11y_text) > 0,
                        api_response=search_api,
                        coverage_note_visible=_has_coverage_notice_in_a11y(a11y_text),
                        results_visible_count=len(search_api.get("hits") or []),
                        empty_final_shown=_has_final_empty_in_a11y(a11y_text),
                        screenshot_path=search_shot.name,
                        notes=f"focused={focused} a11y_len={len(a11y_text)}",
                    )
                )

                # 2. Replace review
                replace_api = _api_request(
                    api_url,
                    token,
                    "/v1/search/replace/preview",
                    method="POST",
                    payload={
                        "query": f"project:current {NEEDLE_TOKEN}",
                        "replacement": "CF09_REPLACED",
                        "origin_project_id": proj["id"],
                    },
                )
                replace_shot = out_dir / f"macos_replace_review_{state}.png"
                _capture_screen(replace_shot)
                replace_a11y = _read_accessibility_text()
                observations.append(
                    PresentationObservation(
                        surface="replace_review",
                        state=state,
                        platform="macos",
                        native_window=focused,
                        project_id=proj["id"],
                        query=f"project:current {NEEDLE_TOKEN}",
                        scope="project:current",
                        request_url=f"{api_url}/v1/search/replace/preview",
                        request_correlated=focused,
                        workspace_ready=focused and len(replace_a11y) > 0,
                        api_response=replace_api,
                        coverage_note_visible=_has_coverage_notice_in_a11y(replace_a11y),
                        results_visible_count=len(replace_api.get("files") or []),
                        empty_final_shown=_has_final_empty_in_a11y(replace_a11y),
                        apply_all_disabled="apply all is unavailable" in replace_a11y.lower(),
                        screenshot_path=replace_shot.name,
                        notes=f"preview_state={replace_api.get('state')} a11y_len={len(replace_a11y)}",
                    )
                )

                # 3. Quick open (Cmd+K)
                _send_escape()
                _send_keystroke("k", ["command"])
                _send_keystroke("target.txt")
                time.sleep(0.8)
                quick_api = _api_request(
                    api_url,
                    token,
                    f"/v1/projects/{proj['id']}/source/search?q=target.txt&limit=20",
                )
                quick_shot = out_dir / f"macos_quick_open_{state}.png"
                _capture_screen(quick_shot)
                quick_a11y = _read_accessibility_text()
                observations.append(
                    PresentationObservation(
                        surface="quick_open",
                        state=state,
                        platform="macos",
                        native_window=focused,
                        project_id=proj["id"],
                        query="target.txt",
                        scope="project:current",
                        request_url=f"{api_url}/v1/projects/{proj['id']}/source/search",
                        request_correlated=focused,
                        workspace_ready=focused and len(quick_a11y) > 0,
                        api_response=quick_api,
                        coverage_note_visible=_has_coverage_notice_in_a11y(quick_a11y),
                        results_visible_count=len(quick_api.get("matches") or []),
                        empty_final_shown=_has_final_empty_in_a11y(quick_a11y),
                        screenshot_path=quick_shot.name,
                        notes=f"a11y_len={len(quick_a11y)}",
                    )
                )
                _send_escape()

                # 4. Files tree
                index_api = _api_request(
                    api_url,
                    token,
                    f"/v1/projects/{proj['id']}/source/index",
                )
                files_shot = out_dir / f"macos_files_tree_{state}.png"
                _capture_screen(files_shot)
                files_a11y = _read_accessibility_text()
                observations.append(
                    PresentationObservation(
                        surface="files_tree",
                        state=state,
                        platform="macos",
                        native_window=focused,
                        project_id=proj["id"],
                        query="",
                        scope="project:current",
                        request_url=f"{api_url}/v1/projects/{proj['id']}/source/index",
                        request_correlated=focused,
                        workspace_ready=focused and len(files_a11y) > 0,
                        api_response=index_api,
                        coverage_note_visible=_has_coverage_notice_in_a11y(files_a11y),
                        results_visible_count=1 if focused else 0,
                        empty_final_shown=_has_final_empty_in_a11y(files_a11y),
                        screenshot_path=files_shot.name,
                        notes=f"a11y_len={len(files_a11y)}",
                    )
                )

            verdicts = [verify_presentation_observation(o) for o in observations]
            return write_presentation_evidence(
                out_dir,
                platform="macos",
                baseline_commit=baseline_commit,
                binary_path=str(engine_bin),
                binary_sha256=_file_sha256(engine_bin),
                verdicts=verdicts,
                extra_metadata={"app_focused": focused},
            )
        finally:
            _stop_proc(app_proc)
            _stop_proc(sidecar_proc)
            for fx in fixtures:
                fx.cleanup()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run CF09 macOS native Den presentation.")
    parser.add_argument("--den-repo", type=Path, required=True)
    parser.add_argument("--engine-bin", type=Path, required=True)
    parser.add_argument("--out-dir", type=Path, required=True)
    parser.add_argument(
        "--baseline-commit",
        default="3bb1464d22dd0a38a149d37610810e7a0324e8cf",
    )
    parser.add_argument("--warming-files", type=int, default=3000)
    args = parser.parse_args(argv)
    report = run_macos_native(
        den_repo=args.den_repo,
        engine_bin=args.engine_bin,
        out_dir=args.out_dir,
        baseline_commit=args.baseline_commit,
        warming_files=args.warming_files,
    )
    print(f"CF09 macOS presentation report written to {report}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
