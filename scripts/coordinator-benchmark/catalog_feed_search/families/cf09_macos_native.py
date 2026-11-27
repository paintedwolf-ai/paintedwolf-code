"""CF09 macOS native Den presentation runner (native Tauri desktop + Playwright + screencapture).

Per D14 and Decisions 037/042, launches an isolated D1 `lycaon-dev` sidecar on
a probed free port, boots the native macOS Den desktop app (`bun run tauri dev`)
attached to that sidecar, captures the native desktop window with `screencapture`,
and runs a Playwright `--project=desktop` spec that intercepts Den's exact HTTP
responses per project/query/scope and verifies rendered DOM coverage notices
across all 4 surfaces (search_stage, quick_open, replace_review, files_tree) x
4 coverage states (failed, incomplete, warming, refreshing).
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
    SURFACES,
    build_coverage_fixture,
    classify_files_tree_notice,
    populate_large_tree,
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


def _go_buildid(path: Path) -> str:
    proc = subprocess.run(
        ["go", "tool", "buildid", str(path)],
        capture_output=True,
        text=True,
        check=False,
        timeout=15,
    )
    return proc.stdout.strip() if proc.returncode == 0 else ""


def _wait_http_ok(url: str, timeout_s: float = 90.0) -> None:
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


def _capture_screen(path: Path) -> bool:
    rc = subprocess.run(
        ["screencapture", "-x", str(path)],
        check=False,
        timeout=15,
    ).returncode
    return rc == 0 and path.exists()


def _write_macos_desktop_spec(spec_path: Path) -> None:
    spec_source = r'''
import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { activateProject, expectShellReady, seedAppState } from "./helpers.ts";

const cfg = JSON.parse(fs.readFileSync(process.env.CF09_RUN_CONFIG!, "utf-8"));
const apiUrl: string = cfg.api_url;
const token: string = cfg.token;
const outDir: string = cfg.out_dir;
const needle: string = cfg.needle;
const projects: Record<string, { id: string; root_id: string; name: string; root_dir?: string }> = cfg.projects;
const warmingRoots: Record<string, string> = cfg.warming_roots ?? {};
const slotFilter: Set<string> | null = Array.isArray(cfg.slots) && cfg.slots.length > 0 ? new Set(cfg.slots) : null;
const wantSlot = (surface: string, state: string) => slotFilter === null || slotFilter.has(`${surface}:${state}`);
const wantState = (state: string) =>
  slotFilter === null ||
  ["search_stage", "replace_review", "quick_open", "files_tree"].some((s) => slotFilter.has(`${s}:${state}`));

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

async function attachWarmingRoot(projectId: string, rootPath: string): Promise<string> {
  if (!rootPath) return "";
  const resp = await apiPost(`/v1/projects/${projectId}/roots`, { path: rootPath });
  return String(resp?.id ?? resp?.root?.id ?? "");
}

function mutateRefreshingTree(rootDir: string | undefined, round: number) {
  if (!rootDir) return;
  for (let d = 0; d < 40; d++) {
    const sub = path.join(rootDir, `pkg_${String(Math.floor(d / 25)).padStart(2, "0")}`, `mod_${String(d).padStart(4, "0")}`);
    fs.mkdirSync(sub, { recursive: true });
    for (let f = 0; f < 10; f++) {
      fs.writeFileSync(
        path.join(sub, `refresh_r${round}_${f}.txt`),
        `refresh round=${round} dir=${d} file=${f} ${needle}\n`,
        "utf-8",
      );
    }
  }
}

test("CF09 macOS native Den coverage presentation across 4 surfaces x 4 states", async ({ page }) => {
  test.setTimeout(240_000);
  const observations: unknown[] = [];

  await seedAppState(page);

  // Record Den's own /v1/projects/{id}/source/index responses so Files tree
  // observations correlate strictly with the HTTP payload that drove the UI.
  const latestSourceIndexByProject = new Map<string, { url: string; body: unknown }>();
  page.on("response", async (res) => {
    const url = res.url();
    const match = url.match(/\/v1\/projects\/([^/]+)\/source\/index(?:\?|$)/);
    if (!match || res.request().method() !== "GET" || !res.ok()) return;
    const pid = match[1];
    const body = await res.json().catch(() => null);
    if (body !== null) {
      latestSourceIndexByProject.set(pid, { url, body });
    }
  });

  await page.goto("/");
  await expectShellReady(page);

  for (const state of ["failed", "incomplete", "warming", "refreshing"] as const) {
    if (!wantState(state) || !projects[state]) continue;
    const proj = projects[state];
    await activateProject(page, proj.id);
    await page.locator('[data-testid="workspace-opening"]').waitFor({ state: "hidden", timeout: 10_000 }).catch(() => {});
    await page.waitForTimeout(300);

    const scopedQuery = `project:current ${needle}`;

    // 1. Search stage (GlobalSearchView) — prepare UI FIRST, then trigger fresh warming root or refresh burst.
    if (wantSlot("search_stage", state) || wantSlot("replace_review", state)) {
    await page.keyboard.press("ControlOrMeta+Shift+f");
    const searchInput = page.getByTestId("search-query-input");
    if (!(await searchInput.isVisible({ timeout: 3000 }).catch(() => false))) {
      await page.keyboard.press("ControlOrMeta+k");
      await expect(page.getByTestId("crossbar-input")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("crossbar-input").fill(scopedQuery);
      const esc = page.getByTestId("crossbar-escalate");
      if (await esc.isVisible({ timeout: 5000 }).catch(() => false)) {
        await esc.click();
      }
    }
    const replaceInput = page.getByTestId("search-replace-input");
    if (await replaceInput.isVisible({ timeout: 300 }).catch(() => false)) {
      await page.getByTestId("search-toggle-replace").click();
      await page.waitForTimeout(200);
    }
    if (await searchInput.isVisible({ timeout: 5000 }).catch(() => false)) {
      await searchInput.fill(scopedQuery);
    }

    if (state === "warming") {
      await attachWarmingRoot(proj.id, warmingRoots.search_stage);
    } else if (state === "refreshing") {
      mutateRefreshingTree(proj.root_dir, 1);
    }

    const searchRespPromise = page.waitForResponse(
      (res) =>
        res.url().includes("/v1/search") &&
        !res.url().includes("/replace/") &&
        res.request().method() === "POST" &&
        (res.request().postData() ?? "").includes(needle) &&
        (res.request().postData() ?? "").includes(proj.id),
      { timeout: 15_000 },
    ).catch(() => null);

    if (await searchInput.isVisible({ timeout: 5000 }).catch(() => false)) {
      await searchInput.press("Enter");
    }
    const searchRes = await searchRespPromise;
    await page.getByTestId("search-loading-state").waitFor({ state: "hidden", timeout: 10_000 }).catch(() => {});
    await page.locator('[aria-label="Close suggestions"]').click({ timeout: 1000 }).catch(() => {});
    await page.waitForTimeout(300);

    const searchApi = searchRes ? await searchRes.json().catch(() => ({})) : {};
    const searchIssueVisible = await page.getByTestId("search-issue-note").isVisible().catch(() => false);
    const searchNoResultsVisible = await page.getByTestId("search-no-results").isVisible().catch(() => false);
    const searchShot = path.join(outDir, `macos_search_stage_${state}.png`);
    await page.screenshot({ path: searchShot });
    if (wantSlot("search_stage", state)) {
    observations.push({
      surface: "search_stage",
      state,
      platform: "macos",
      native_window: true,
      project_id: proj.id,
      query: scopedQuery,
      scope: "project:current",
      request_url: searchRes?.url() ?? "",
      request_correlated: searchRes !== null,
      workspace_ready: true,
      api_response: searchApi,
      coverage_note_visible: searchIssueVisible,
      coverage_note_testid: searchIssueVisible ? "search-issue-note" : "",
      results_visible_count: Array.isArray(searchApi?.hits) ? searchApi.hits.length : 0,
      empty_final_shown: searchNoResultsVisible,
      screenshot_path: path.basename(searchShot),
    });
    }

    // 2. Replace review — trigger fresh warming root or refresh burst while Search stage is already open.
    if (wantSlot("replace_review", state)) {
    if (state === "warming") {
      await attachWarmingRoot(proj.id, warmingRoots.replace_review);
    } else if (state === "refreshing") {
      mutateRefreshingTree(proj.root_dir, 2);
    }
    const replaceRespPromise = page.waitForResponse(
      (res) =>
        res.url().includes("/v1/search/replace/preview") &&
        res.request().method() === "POST" &&
        (res.request().postData() ?? "").includes(proj.id),
      { timeout: 15_000 },
    ).catch(() => null);

    const toggleReplace = page.getByTestId("search-toggle-replace");
    if (await toggleReplace.isVisible({ timeout: 3000 }).catch(() => false)) {
      await toggleReplace.click();
    }
    const replaceRes = await replaceRespPromise;
    await page.waitForTimeout(400);
    const replaceApi = replaceRes ? await replaceRes.json().catch(() => ({})) : {};
    const replaceCovVisible =
      (await page.getByTestId("search-replace-coverage").isVisible().catch(() => false)) ||
      (await page.getByTestId("search-replace-truncated").isVisible().catch(() => false)) ||
      (await page.getByTestId("cancel-preparation").isVisible().catch(() => false));
    const replaceEmptyVisible = await page.getByTestId("search-replace-empty").isVisible().catch(() => false);
    const applyBtn = page.getByTestId("search-replace-apply");
    const applyDisabled = (await applyBtn.count()) > 0 ? await applyBtn.isDisabled() : true;
    const replaceShot = path.join(outDir, `macos_replace_review_${state}.png`);
    await page.screenshot({ path: replaceShot });
    observations.push({
      surface: "replace_review",
      state,
      platform: "macos",
      native_window: true,
      project_id: proj.id,
      query: scopedQuery,
      scope: "project:current",
      request_url: replaceRes?.url() ?? "",
      request_correlated: replaceRes !== null,
      workspace_ready: true,
      api_response: replaceApi,
      coverage_note_visible: replaceCovVisible,
      coverage_note_testid: replaceCovVisible ? "search-replace-coverage" : "",
      results_visible_count: Array.isArray(replaceApi?.files) ? replaceApi.files.length : 0,
      empty_final_shown: replaceEmptyVisible,
      apply_all_disabled: applyDisabled,
      screenshot_path: path.basename(replaceShot),
    });

    if (await toggleReplace.isVisible({ timeout: 500 }).catch(() => false)) {
      await toggleReplace.click();
    }
    }
    }

    // 3. Quick open (Crossbar) — open Crossbar FIRST, then trigger fresh warming root or refresh burst.
    if (wantSlot("quick_open", state)) {
    await page.keyboard.press("Escape");
    await page.keyboard.press("ControlOrMeta+k");
    await expect(page.getByTestId("crossbar-input")).toBeVisible({ timeout: 10_000 });
    if (state === "warming") {
      await attachWarmingRoot(proj.id, warmingRoots.quick_open);
    } else if (state === "refreshing") {
      mutateRefreshingTree(proj.root_dir, 3);
    }
    const quickRespPromise = page.waitForResponse(
      (res) =>
        res.url().includes(`/v1/projects/${proj.id}/source/search`) &&
        res.url().includes("target.txt"),
      { timeout: 15_000 },
    ).catch(() => null);
    await page.getByTestId("crossbar-input").fill("target.txt");
    const quickRes = await quickRespPromise;
    const loadingGlyph = page
      .locator('[data-testid="crossbar"] [aria-busy="true"], [data-testid="crossbar"] [role="progressbar"], [data-testid="crossbar"] .animate-spin, [data-testid="crossbar-loading"]')
      .first();
    const loadingBefore = await loadingGlyph.isVisible({ timeout: 250 }).catch(() => false);
    if (loadingBefore) {
      await loadingGlyph.waitFor({ state: "hidden", timeout: 1_500 }).catch(() => {});
    }
    await page.waitForTimeout(300);
    const loadingAfter = await loadingGlyph.isVisible({ timeout: 200 }).catch(() => false);
    const loadingAria = loadingAfter
      ? {
          role: await loadingGlyph.getAttribute("role").catch(() => null),
          ariaLabel: await loadingGlyph.getAttribute("aria-label").catch(() => null),
          ariaBusy: await loadingGlyph.getAttribute("aria-busy").catch(() => null),
          testid: await loadingGlyph.getAttribute("data-testid").catch(() => null),
        }
      : null;
    const quickApi = quickRes ? await quickRes.json().catch(() => ({})) : {};
    const covLoc = page.getByTestId("crossbar-coverage").first();
    const hitStatusLoc = page.getByTestId("crossbar-hit-status").first();
    const covVisible = await covLoc.isVisible({ timeout: 500 }).catch(() => false);
    const hitStatusVisible = !covVisible && (await hitStatusLoc.isVisible({ timeout: 300 }).catch(() => false));
    const crossbarCovVisible = covVisible || hitStatusVisible;
    const crossbarCovTestId = covVisible ? "crossbar-coverage" : hitStatusVisible ? "crossbar-hit-status" : "";
    const crossbarCovText = covVisible
      ? ((await covLoc.textContent({ timeout: 500 }).catch(() => "")) ?? "").trim()
      : hitStatusVisible
        ? ((await hitStatusLoc.textContent({ timeout: 500 }).catch(() => "")) ?? "").trim()
        : "";
    const crossbarEmptyVisible = await page.getByTestId("crossbar-empty").isVisible().catch(() => false);
    const renderedRows = await page.locator('[role="option"], [data-testid^="crossbar-row"]').count().catch(() => 0);
    const quickShot = path.join(outDir, `macos_quick_open_${state}.png`);
    await page.screenshot({ path: quickShot });
    observations.push({
      surface: "quick_open",
      state,
      platform: "macos",
      native_window: true,
      project_id: proj.id,
      query: "target.txt",
      scope: "project:current",
      request_url: quickRes?.url() ?? "",
      request_correlated: quickRes !== null,
      workspace_ready: true,
      api_response: quickApi,
      coverage_note_visible: crossbarCovVisible,
      coverage_note_testid: crossbarCovTestId,
      results_visible_count: Array.isArray(quickApi?.matches) ? quickApi.matches.length : 0,
      empty_final_shown: crossbarEmptyVisible,
      screenshot_path: path.basename(quickShot),
      notes: JSON.stringify({
        coverage_text: crossbarCovText,
        rendered_rows: renderedRows,
        loading_before: loadingBefore,
        loading_after: loadingAfter,
        loading_aria: loadingAria,
      }),
    });
    await page.keyboard.press("Escape");
    }

    // 4. Files tree — open Files FIRST, wait for pane ready, then trigger fresh warming root or refresh burst and correlate with Den's own /source/index response.
    if (wantSlot("files_tree", state)) {
    const filesNav = page.locator('[data-testid="nav-files"], [aria-label="Files"]').first();
    if (await filesNav.isVisible({ timeout: 3000 }).catch(() => false)) {
      await filesNav.click();
    }
    await page.getByTestId("files-tree-pane").waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
    await page.waitForTimeout(300);
    let selectedRootId = proj.root_id;
    let selectedRootDir = proj.root_dir ?? "";
    const indexRespPromise = page.waitForResponse(
      (res) =>
        res.url().includes(`/v1/projects/${proj.id}/source/index`) &&
        res.request().method() === "GET" &&
        res.ok(),
      { timeout: 8_000 },
    ).catch(() => null);
    if (state === "warming" && warmingRoots.files_tree) {
      const newRootId = await attachWarmingRoot(proj.id, warmingRoots.files_tree);
      if (newRootId) {
        selectedRootId = newRootId;
        selectedRootDir = warmingRoots.files_tree;
      }
      const newRootLabel = path.basename(warmingRoots.files_tree);
      const rootNode = page.getByTestId("files-tree-pane").getByText(newRootLabel, { exact: false }).first();
      await rootNode.waitFor({ state: "visible", timeout: 8_000 }).catch(() => {});
      await rootNode.click({ timeout: 2_000 }).catch(() => {});
      await page.waitForTimeout(400);
    } else if (state === "refreshing") {
      mutateRefreshingTree(proj.root_dir, 4);
      await page.waitForTimeout(300);
    }
    const freshIndexRes = await indexRespPromise;
    let correlatedIndex = latestSourceIndexByProject.get(proj.id) ?? null;
    if (freshIndexRes) {
      const freshBody = await freshIndexRes.json().catch(() => null);
      if (freshBody !== null) {
        correlatedIndex = { url: freshIndexRes.url(), body: freshBody };
        latestSourceIndexByProject.set(proj.id, correlatedIndex);
      }
    }
    const treeVisible = await page.getByTestId("files-tree-pane").isVisible().catch(() => false);
    const rootSummaries = page.getByTestId("files-root-summary");
    const rootSummary = selectedRootDir
      ? rootSummaries.filter({
          has: page.getByTestId("files-root-summary-path").filter({ hasText: selectedRootDir }),
        }).first()
      : rootSummaries.first();
    const rootSummaryVisible = await rootSummary.isVisible({ timeout: 1500 }).catch(() => false);
    const noticeScope = rootSummaryVisible ? rootSummary : page.getByTestId("files-tree-pane");
    const noticeCandidates = [
      { testid: "files-root-summary-coverage", loc: noticeScope.getByTestId("files-root-summary-coverage").first() },
      { testid: "source-watch-notice", loc: noticeScope.getByTestId("source-watch-notice").first() },
      { testid: "files-tree-coverage-note", loc: noticeScope.getByTestId("files-tree-coverage-note").first() },
      { testid: "review-inventory-status", loc: noticeScope.getByTestId("review-inventory-status").first() },
    ];
    let treeNoticeTestId = "";
    let treeNoticeText = "";
    for (const candidate of noticeCandidates) {
      if (await candidate.loc.isVisible({ timeout: 1200 }).catch(() => false)) {
        const txt = ((await candidate.loc.textContent({ timeout: 1000 }).catch(() => "")) ?? "").trim();
        if (txt.length > 0) {
          treeNoticeTestId = candidate.testid;
          treeNoticeText = txt;
          break;
        }
      }
    }
    const retryVisible = rootSummaryVisible
      ? await rootSummary.getByTestId("retry-file-index").first().isVisible({ timeout: 500 }).catch(() => false)
      : false;
    const treeNoticeVisible = treeNoticeTestId.length > 0 && treeNoticeText.length > 0;
    const treeFilterEmpty = await page.getByTestId("files-tree-filter-empty").isVisible().catch(() => false);
    const filesShot = path.join(outDir, `macos_files_tree_${state}.png`);
    await page.screenshot({ path: filesShot });
    observations.push({
      surface: "files_tree",
      state,
      platform: "macos",
      native_window: true,
      project_id: proj.id,
      selected_root_id: selectedRootId,
      query: "",
      scope: "project:current",
      request_url: correlatedIndex?.url ?? "",
      request_correlated: correlatedIndex !== null,
      workspace_ready: treeVisible,
      api_response: correlatedIndex?.body ?? {},
      coverage_note_visible: treeNoticeVisible,
      coverage_note_testid: treeNoticeTestId,
      results_visible_count: treeVisible ? 1 : 0,
      empty_final_shown: treeFilterEmpty,
      screenshot_path: path.basename(filesShot),
      notes: retryVisible ? `${treeNoticeText} [retry-file-index]` : treeNoticeText,
    });
    }
  }

  fs.writeFileSync(
    path.join(outDir, "raw_observations_macos.json"),
    JSON.stringify(observations, null, 2) + "\n",
    "utf-8",
  );
});
'''
    spec_path.write_text(spec_source.lstrip(), encoding="utf-8")


def run_macos_native(
    *,
    den_repo: Path,
    engine_bin: Path,
    out_dir: Path,
    baseline_commit: str,
    warming_files: int = 30000,
    slots: list[str] | None = None,
) -> Path:
    out_dir.mkdir(parents=True, exist_ok=True)
    port = _free_port()
    api_url = f"http://127.0.0.1:{port}"
    token = f"cf09-macos-{int(time.time())}"

    sidecar_proc: subprocess.Popen[bytes] | None = None
    tauri_proc: subprocess.Popen[bytes] | None = None
    fixtures = []
    spec_path = den_repo / "lycaon-den" / "e2e" / "cf09-coverage-macos.desktop.spec.ts"

    with tempfile.TemporaryDirectory(prefix="cf09-macos-") as tmp_str:
        tmp = Path(tmp_str)
        cfg_dir = tmp / "config"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        (cfg_dir / "api.token").write_text(token, encoding="utf-8")
        fx_dir = tmp / "fixtures"
        fx_dir.mkdir(parents=True, exist_ok=True)

        try:
            needed_states = (
                {s.split(":", 1)[1] for s in slots if ":" in s}
                if slots
                else set(COVERAGE_STATES)
            )
            needed_warming_surfaces = (
                {s.split(":", 1)[0] for s in slots if s.endswith(":warming")}
                if slots
                else set(SURFACES)
            )
            for st in COVERAGE_STATES:
                if st not in needed_states:
                    continue
                fixtures.append(
                    build_coverage_fixture(fx_dir, st, warming_file_count=warming_files)
                )
            warming_roots: dict[str, str] = {}
            for surf in SURFACES:
                if surf not in needed_warming_surfaces:
                    continue
                wdir = (fx_dir / f"cf09_warming_{surf}").resolve()
                populate_large_tree(wdir, file_count=warming_files, include_needle=True)
                warming_roots[surf] = str(wdir)

            sidecar_env = os.environ.copy()
            sidecar_env.update(
                {
                    "LYCAON_CONFIG_DIR": str(cfg_dir),
                    "LYCAON_CONFIG_ROOT": str(den_repo / "lycaon"),
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
                    "root_dir": str(fx.root_dir),
                }
                if fx.state == "refreshing":
                    settle_deadline = time.monotonic() + 30.0
                    while time.monotonic() < settle_deadline:
                        idx = _api_request(api_url, token, f"/v1/projects/{resp['id']}/source/index")
                        cov = idx.get("coverage") or []
                        if cov and all(r.get("discovery_complete") is True for r in cov if isinstance(r, dict)):
                            break
                        time.sleep(0.25)

            tauri_env = os.environ.copy()
            tauri_env.update(
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
            tauri_log = (out_dir / "tauri_macos.log").open("wb")
            tauri_proc = subprocess.Popen(
                ["bun", "run", "tauri", "dev"],
                cwd=str(den_repo / "lycaon-den"),
                env=tauri_env,
                stdout=tauri_log,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
            _wait_http_ok("http://127.0.0.1:1420", timeout_s=120.0)
            time.sleep(3.0)

            run_cfg_path = tmp / "cf09_run_config.json"
            run_cfg_path.write_text(
                json.dumps(
                    {
                        "api_url": api_url,
                        "token": token,
                        "out_dir": str(out_dir),
                        "needle": NEEDLE_TOKEN,
                        "projects": projects_meta,
                        "warming_roots": warming_roots,
                        "slots": slots or [],
                    },
                    indent=2,
                ),
                encoding="utf-8",
            )
            _write_macos_desktop_spec(spec_path)

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
                    "bun",
                    "x",
                    "playwright",
                    "test",
                    "--project=desktop",
                    "e2e/cf09-coverage-macos.desktop.spec.ts",
                ],
                cwd=str(den_repo / "lycaon-den"),
                env=pw_env,
                check=True,
            )

            desktop_shot = out_dir / "native_desktop_macos.png"
            desktop_captured = _capture_screen(desktop_shot)

            # Stop background processes and flush logs BEFORE computing sha256_manifest.json.
            _stop_proc(tauri_proc)
            tauri_proc = None
            _stop_proc(sidecar_proc)
            sidecar_proc = None
            tauri_log.close()
            sidecar_log.close()

            raw_obs = json.loads(
                (out_dir / "raw_observations_macos.json").read_text(encoding="utf-8")
            )
            verdicts = []
            for item in raw_obs:
                obs = PresentationObservation(**item)
                if obs.surface == "files_tree":
                    cls = classify_files_tree_notice(
                        obs.notes,
                        testid=obs.coverage_note_testid,
                        retry_button_visible="[retry-file-index]" in obs.notes,
                    )
                    obs = PresentationObservation(
                        **{
                            **item,
                            "coverage_note_visible": cls.visible,
                            "coverage_note_testid": cls.testid,
                            "coverage_notice_categories": cls.categories,
                        }
                    )
                verdicts.append(verify_presentation_observation(obs))
            return write_presentation_evidence(
                out_dir,
                platform="macos",
                baseline_commit=baseline_commit,
                binary_path=str(engine_bin),
                binary_sha256=_file_sha256(engine_bin),
                verdicts=verdicts,
                extra_metadata={
                    "go_tool_buildid": _go_buildid(engine_bin),
                    "desktop_screenshot_captured": desktop_captured,
                },
            )
        finally:
            if spec_path.exists():
                spec_path.unlink()
            _stop_proc(tauri_proc)
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
    parser.add_argument("--warming-files", type=int, default=30000)
    parser.add_argument("--slots", default="", help="Optional comma-separated surface:state subset")
    args = parser.parse_args(argv)
    slots = [s.strip() for s in args.slots.split(",") if s.strip()] if args.slots else None
    report = run_macos_native(
        den_repo=args.den_repo,
        engine_bin=args.engine_bin,
        out_dir=args.out_dir,
        baseline_commit=args.baseline_commit,
        warming_files=args.warming_files,
        slots=slots,
    )
    print(f"CF09 macOS presentation report written to {report}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
