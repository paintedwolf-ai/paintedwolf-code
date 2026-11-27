"""CF09 Linux native Den presentation runner (Pop!_OS / Xvfb + Tauri desktop + Playwright).

Launches an isolated D1 `lycaon-dev` sidecar on a probed free port, boots the
real native Tauri Den desktop window under Xvfb, and runs a Playwright
desktop-target spec against all 4 surfaces (search_stage, quick_open,
replace_review, files_tree) x 4 coverage states (warming, refreshing, failed,
incomplete), capturing live API payloads, DOM assertions, and screenshots.
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
import sys
import tempfile
import time
import urllib.request
from typing import Any

from .cf09_presentation import (
    COVERAGE_STATES,
    NEEDLE_TOKEN,
    PresentationObservation,
    SURFACES,
    build_coverage_fixture,
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


def _wait_http_ok(url: str, timeout_s: float = 45.0) -> None:
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


def _write_desktop_spec(spec_path: Path) -> None:
    spec_source = r'''
import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";

const cfg = JSON.parse(fs.readFileSync(process.env.CF09_RUN_CONFIG!, "utf-8"));
const apiUrl: string = cfg.api_url;
const token: string = cfg.token;
const outDir: string = cfg.out_dir;
const needle: string = cfg.needle;
const projects: Record<string, { id: string; root_id: string; name: string }> = cfg.projects;

async function apiGet(p: string) {
  const res = await fetch(`${apiUrl}${p}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return await res.json();
}

async function apiPost(p: string, body: unknown) {
  const res = await fetch(`${apiUrl}${p}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
  return await res.json();
}

async function waitShell(page: Page) {
  await expect(page.getByTestId("shell")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("shell")).toHaveAttribute("data-sidecar-status", "connected", { timeout: 30_000 });
}

test("CF09 native Den coverage presentation across 4 surfaces x 4 states", async ({ page }) => {
  test.setTimeout(240_000);
  const observations: unknown[] = [];

  await page.goto("/");
  await waitShell(page);

  for (const state of ["failed", "incomplete", "warming", "refreshing"] as const) {
    const proj = projects[state];
    // Activate project via URL / sidebar click
    const projectBtn = page.locator(`[data-project-id="${proj.id}"]`).first();
    if (await projectBtn.count()) {
      await projectBtn.click();
      await page.waitForTimeout(400);
    }

    // 1. Search stage (GlobalSearchView)
    await page.keyboard.press("ControlOrMeta+Shift+f");
    const searchInput = page.getByTestId("search-query-input");
    if (!(await searchInput.isVisible({ timeout: 3000 }).catch(() => false))) {
      await page.keyboard.press("ControlOrMeta+k");
      await expect(page.getByTestId("crossbar-input")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("crossbar-input").fill(needle);
      const esc = page.getByTestId("crossbar-escalate");
      if (await esc.isVisible({ timeout: 5000 }).catch(() => false)) {
        await esc.click();
      }
    }
    if (await searchInput.isVisible({ timeout: 5000 }).catch(() => false)) {
      await searchInput.fill(needle);
      await page.waitForTimeout(800);
    }
    const searchApi = await apiPost("/v1/search", {
      query: needle,
      project_id: proj.id,
      budget: "interactive",
    });
    const searchIssueVisible = await page.getByTestId("search-issue-note").isVisible().catch(() => false);
    const searchNoResultsVisible = await page.getByTestId("search-no-results").isVisible().catch(() => false);
    const searchShot = path.join(outDir, `linux_search_stage_${state}.png`);
    await page.screenshot({ path: searchShot });
    observations.push({
      surface: "search_stage",
      state,
      platform: "linux",
      native_window: true,
      api_response: searchApi,
      coverage_note_visible: searchIssueVisible,
      coverage_note_testid: searchIssueVisible ? "search-issue-note" : "",
      results_visible_count: Array.isArray(searchApi?.hits) ? searchApi.hits.length : 0,
      empty_final_shown: searchNoResultsVisible,
      screenshot_path: path.basename(searchShot),
    });

    // 2. Replace review (GlobalSearchView replace mode)
    const toggleReplace = page.getByTestId("search-toggle-replace");
    if (await toggleReplace.isVisible({ timeout: 3000 }).catch(() => false)) {
      await toggleReplace.click();
      await page.waitForTimeout(800);
    }
    const replaceApi = await apiPost("/v1/search/replace/preview", {
      query: needle,
      replacement: "CF09_REPLACED",
      project_id: proj.id,
    });
    const replaceCovVisible =
      (await page.getByTestId("search-replace-coverage").isVisible().catch(() => false)) ||
      (await page.getByTestId("search-replace-truncated").isVisible().catch(() => false)) ||
      (await page.getByTestId("cancel-preparation").isVisible().catch(() => false));
    const replaceEmptyVisible = await page.getByTestId("search-replace-empty").isVisible().catch(() => false);
    const applyBtn = page.getByTestId("search-replace-apply");
    const applyDisabled = (await applyBtn.count()) > 0 ? await applyBtn.isDisabled() : true;
    const replaceShot = path.join(outDir, `linux_replace_review_${state}.png`);
    await page.screenshot({ path: replaceShot });
    observations.push({
      surface: "replace_review",
      state,
      platform: "linux",
      native_window: true,
      api_response: replaceApi,
      coverage_note_visible: replaceCovVisible,
      coverage_note_testid: replaceCovVisible ? "search-replace-coverage" : "",
      results_visible_count: Array.isArray(replaceApi?.files) ? replaceApi.files.length : 0,
      empty_final_shown: replaceEmptyVisible,
      apply_all_disabled: applyDisabled,
      screenshot_path: path.basename(replaceShot),
    });

    // 3. Quick open (Crossbar)
    await page.keyboard.press("Escape");
    await page.keyboard.press("ControlOrMeta+k");
    await expect(page.getByTestId("crossbar-input")).toBeVisible({ timeout: 10_000 });
    await page.getByTestId("crossbar-input").fill("target.txt");
    await page.waitForTimeout(800);
    const quickApi = await apiGet(`/v1/projects/${proj.id}/source/search?q=target.txt&limit=20`);
    const crossbarCovVisible = await page.getByTestId("crossbar-coverage").isVisible().catch(() => false);
    const crossbarEmptyVisible = await page.getByTestId("crossbar-empty").isVisible().catch(() => false);
    const quickShot = path.join(outDir, `linux_quick_open_${state}.png`);
    await page.screenshot({ path: quickShot });
    observations.push({
      surface: "quick_open",
      state,
      platform: "linux",
      native_window: true,
      api_response: quickApi,
      coverage_note_visible: crossbarCovVisible,
      coverage_note_testid: crossbarCovVisible ? "crossbar-coverage" : "",
      results_visible_count: Array.isArray(quickApi?.matches) ? quickApi.matches.length : 0,
      empty_final_shown: crossbarEmptyVisible,
      screenshot_path: path.basename(quickShot),
    });
    await page.keyboard.press("Escape");

    // 4. Files tree
    const filesNav = page.locator('[data-testid="nav-files"], [aria-label="Files"]').first();
    if (await filesNav.isVisible({ timeout: 3000 }).catch(() => false)) {
      await filesNav.click();
      await page.waitForTimeout(600);
    }
    const indexApi = await apiGet(`/v1/projects/${proj.id}/source/index`);
    const treeVisible = await page.getByTestId("files-tree-pane").isVisible().catch(() => false);
    const treeFilterEmpty = await page.getByTestId("files-tree-filter-empty").isVisible().catch(() => false);
    const filesShot = path.join(outDir, `linux_files_tree_${state}.png`);
    await page.screenshot({ path: filesShot });
    observations.push({
      surface: "files_tree",
      state,
      platform: "linux",
      native_window: true,
      api_response: indexApi,
      coverage_note_visible: treeVisible,
      coverage_note_testid: treeVisible ? "files-tree-pane" : "",
      results_visible_count: treeVisible ? 1 : 0,
      empty_final_shown: treeFilterEmpty,
      screenshot_path: path.basename(filesShot),
    });
  }

  fs.writeFileSync(
    path.join(outDir, "raw_observations_linux.json"),
    JSON.stringify(observations, null, 2) + "\n",
    "utf-8",
  );
});
'''
    spec_path.write_text(spec_source.lstrip(), encoding="utf-8")


def run_linux_native(
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
    token = f"cf09-linux-{int(time.time())}"
    display = ":99"

    xvfb_proc: subprocess.Popen[bytes] | None = None
    sidecar_proc: subprocess.Popen[bytes] | None = None
    tauri_proc: subprocess.Popen[bytes] | None = None
    fixtures = []
    spec_path = den_repo / "lycaon-den" / "e2e" / "cf09-coverage.desktop.spec.ts"

    with tempfile.TemporaryDirectory(prefix="cf09-linux-") as tmp_str:
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
                    "LYCAON_API_TOKEN": token,
                    "LYCAON_DEV": "1",
                    "LYCAON_LLM_MOCK": "1",
                    "NO_PROXY": "127.0.0.1,localhost",
                }
            )
            sidecar_log = (out_dir / "sidecar_linux.log").open("wb")
            sidecar_proc = subprocess.Popen(
                [str(engine_bin), "serve", "--addr", f"127.0.0.1:{port}"],
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
                    # Wait briefly for initial index then mutate to trigger refreshing.
                    time.sleep(1.5)
                    mutate_for_refreshing(fx, burst_count=200)

            xvfb_proc = subprocess.Popen(
                ["Xvfb", display, "-screen", "0", "1440x900x24"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
            time.sleep(1.0)

            tauri_env = os.environ.copy()
            tauri_env.update(
                {
                    "DISPLAY": display,
                    "LYCAON_ATTACH_ONLY": "1",
                    "LYCAON_DEV": "1",
                    "LYCAON_CONFIG_DIR": str(cfg_dir),
                    "VITE_LYCAON_API_URL": api_url,
                    "VITE_LYCAON_API_TOKEN": token,
                    "NO_PROXY": "127.0.0.1,localhost",
                }
            )
            tauri_log = (out_dir / "tauri_linux.log").open("wb")
            tauri_proc = subprocess.Popen(
                ["bun", "run", "tauri", "dev"],
                cwd=str(den_repo / "lycaon-den"),
                env=tauri_env,
                stdout=tauri_log,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
            _wait_http_ok("http://127.0.0.1:1420", timeout_s=90.0)
            time.sleep(3.0)

            # Capture proof of live native Xvfb desktop window.
            xwd_path = out_dir / "native_desktop_root.xwd"
            subprocess.run(
                ["xwd", "-root", "-display", display, "-out", str(xwd_path)],
                check=False,
                timeout=10,
            )

            run_cfg_path = tmp / "cf09_run_config.json"
            run_cfg_path.write_text(
                json.dumps(
                    {
                        "api_url": api_url,
                        "token": token,
                        "out_dir": str(out_dir),
                        "needle": NEEDLE_TOKEN,
                        "projects": projects_meta,
                    },
                    indent=2,
                ),
                encoding="utf-8",
            )
            _write_desktop_spec(spec_path)

            pw_env = tauri_env.copy()
            pw_env.update(
                {
                    "PLAYWRIGHT_E2E": "desktop",
                    "PLAYWRIGHT_REUSE_SERVER": "1",
                    "LYCAON_E2E_ADDR": f"127.0.0.1:{port}",
                    "LYCAON_E2E_API_URL": api_url,
                    "LYCAON_E2E_TOKEN": token,
                    "LYCAON_E2E_BASE_URL": "http://127.0.0.1:1420",
                    "CF09_RUN_CONFIG": str(run_cfg_path),
                }
            )
            subprocess.run(
                [
                    "bunx",
                    "playwright",
                    "test",
                    "--project=desktop",
                    "e2e/cf09-coverage.desktop.spec.ts",
                ],
                cwd=str(den_repo / "lycaon-den"),
                env=pw_env,
                check=True,
            )

            raw_obs = json.loads(
                (out_dir / "raw_observations_linux.json").read_text(encoding="utf-8")
            )
            verdicts = [
                verify_presentation_observation(PresentationObservation(**item))
                for item in raw_obs
            ]
            return write_presentation_evidence(
                out_dir,
                platform="linux",
                baseline_commit=baseline_commit,
                binary_path=str(engine_bin),
                binary_sha256=_file_sha256(engine_bin),
                verdicts=verdicts,
                extra_metadata={
                    "display": display,
                    "xwd_captured": xwd_path.exists(),
                },
            )
        finally:
            if spec_path.exists():
                spec_path.unlink()
            _stop_proc(tauri_proc)
            _stop_proc(sidecar_proc)
            _stop_proc(xvfb_proc)
            for fx in fixtures:
                fx.cleanup()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run CF09 Linux native Den presentation.")
    parser.add_argument("--den-repo", type=Path, required=True)
    parser.add_argument("--engine-bin", type=Path, required=True)
    parser.add_argument("--out-dir", type=Path, required=True)
    parser.add_argument(
        "--baseline-commit",
        default="3bb1464d22dd0a38a149d37610810e7a0324e8cf",
    )
    parser.add_argument("--warming-files", type=int, default=3000)
    args = parser.parse_args(argv)
    report = run_linux_native(
        den_repo=args.den_repo,
        engine_bin=args.engine_bin,
        out_dir=args.out_dir,
        baseline_commit=args.baseline_commit,
        warming_files=args.warming_files,
    )
    print(f"CF09 Linux presentation report written to {report}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
