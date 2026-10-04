import { randomUUID } from "node:crypto";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiConfig, modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";
import type { CreateSessionRequest, ProjectSourceReadResponse, SourceViewCreate, SourceWalkResponse, SourceWorkspace } from "../src/api/types.ts";

modelIndependentWebE2e.use({ browserName: "webkit" });

modelIndependentWebE2e("Walk publishes a delayed file selection and can reselect it", async ({ page, request }) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "walk-file-navigation", name: "Walk file navigation",
    seed(root) {
      for (const name of ["a.ts", "b.ts", "unrelated.ts"]) {
        writeFileSync(path.join(root, name), `export const initial = "${name}";\n`);
      }
    },
  });
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const base = `${apiUrl}/v1/projects/${project.id}/source`;
  const rootId = project.roots[0]!.id;
  await expect.poll(async () => {
    const response = await request.get(`${base}/workspace`, { headers });
    expect(response.ok()).toBeTruthy();
    return ((await response.json()) as SourceWorkspace).inventory.complete;
  }).toBe(true);
  const pinned = await request.post(`${base}/pins`, { headers, data: { label: "Before edits" } });
  expect(pinned.ok(), await pinned.text()).toBeTruthy();
  const { id: pinId } = await pinned.json() as { id: string };
  for (const name of ["a.ts", "b.ts"]) {
    const response = await request.get(base, { headers, params: { root_id: rootId, path: name } });
    expect(response.ok(), await response.text()).toBeTruthy();
    const source = await response.json() as ProjectSourceReadResponse;
    const saved = await request.put(base, { headers, data: {
      operation_id: randomUUID(), root_id: rootId, path: name,
      base_sha256: source.sha256, encoding: source.encoding,
      content: `export const updated = "${name}";\n`,
    } });
    expect(saved.ok(), await saved.text()).toBeTruthy();
  }
  const sessionResponse = await request.post(`${apiUrl}/v1/sessions`, {
    headers, data: { project_id: project.id, posture: "build" } satisfies CreateSessionRequest,
  });
  expect(sessionResponse.ok(), await sessionResponse.text()).toBeTruthy();
  const { id: sessionId } = await sessionResponse.json() as { id: string };
  const walkResponse = await request.get(`${base}/walk`, { headers, params: { baseline: `pin:${pinId}`, limit: 500 } });
  expect(walkResponse.ok(), await walkResponse.text()).toBeTruthy();
  const secondEffectId = ((await walkResponse.json()) as SourceWalkResponse).files
    .find((file) => file.path === "b.ts")?.effects[0]?.id;
  expect(secondEffectId).toBeTruthy();

  // Walk warms neighboring comparisons as source views on entry, so the hold precedes it.
  let releaseComparison!: () => void;
  const comparisonReady = new Promise<void>((resolve) => { releaseComparison = resolve; });
  let comparisonRequested = false;
  await page.route("**/source/views", async (route) => {
    const create = route.request().method() === "POST" ? route.request().postDataJSON() as SourceViewCreate : null;
    if (create?.kind === "comparison" && create.source.kind === "effect" && create.source.effect_id === secondEffectId) {
      comparisonRequested = true;
      await comparisonReady;
    }
    await route.continue();
  });

  let releaseSources!: () => void;
  const sourcesReady = new Promise<void>((resolve) => { releaseSources = resolve; });
  await page.route("**/source?*", async (route) => {
    if (route.request().method() === "GET") await sourcesReady;
    await route.continue();
  });
  try {
    await page.evaluate(async ({ projectId, rootId, sessionId, pinId }) => {
      const connectionUrl = "/src/platform/connection/app-connection.ts";
      const walkUrl = "/src/files/walk/walk-store.ts";
      const buffersUrl = "/src/files/documents/project-files-buffers.ts";
      const connection = await import(/* @vite-ignore */ connectionUrl) as typeof import("../src/platform/connection/app-connection.ts");
      const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
      const buffers = await import(/* @vite-ignore */ buffersUrl) as typeof import("../src/files/documents/project-files-buffers.ts");
      const client = connection.getLycaonClient()!;
      for (const name of ["a.ts", "unrelated.ts"]) {
        buffers.openFilesBuffer(projectId, { rootId, rootLabel: "Project root", path: name, intent: "permanent" });
      }
      const state = await walk.enterWalk(projectId, {
        ...client,
        listProjectSourceWalk: (id, options) => client.listProjectSourceWalk(id, { ...options, baseline: `pin:${pinId}`, sessionId: undefined, includeOutsideChanges: undefined }),
      }, sessionId);
      if (state.status !== "ready") throw new Error(state.notice ?? "Walk did not become ready");
      if (state.walk.steps.length !== 2) throw new Error(`Expected two recorded edits, got ${state.walk.steps.length}`);
    }, { projectId: project.id, rootId, sessionId, pinId });
    const rail = page.getByTestId("step-bar-line");
    await expect(rail).toBeVisible();
    await expect.poll(async () => rail.evaluate((line) => {
      const dots = [...line.parentElement!.querySelectorAll<HTMLElement>(".den-step-bar__dot")];
      const centers = dots.map((dot) => {
        const box = dot.getBoundingClientRect();
        return box.left + box.width / 2;
      });
      const bounds = line.getBoundingClientRect();
      if (dots.length !== 2 || bounds.height !== 1) return Number.POSITIVE_INFINITY;
      return Math.max(
        Math.abs(bounds.left - centers[0]!),
        Math.abs(bounds.right - centers[centers.length - 1]!),
      );
    })).toBeLessThan(0.5);
    const selected = page.locator('[data-testid="files-tab"][aria-selected="true"]');
    await expect(selected).toHaveAttribute("data-path", "a.ts");
    await expect(page.getByTestId("file-version-document").filter({ visible: true })).toContainText('"a.ts"');

    const selectSecond = async () => page.evaluate(async (projectId) => {
      const url = "/src/files/walk/walk-store.ts";
      const walk = await import(/* @vite-ignore */ url) as typeof import("../src/files/walk/walk-store.ts");
      walk.setWalkAt(projectId, 1);
    }, project.id);
    try {
      await selectSecond();
      await expect.poll(() => comparisonRequested).toBe(true);
      await expect(selected).toHaveAttribute("data-path", "a.ts");
    } finally {
      releaseComparison();
    }
    await expect(selected).toHaveAttribute("data-path", "b.ts");
    await expect(page.getByTestId("file-version-document").filter({ visible: true })).toContainText('"b.ts"');
    releaseSources();
    await page.getByTestId("files-tree-file").filter({ hasText: "unrelated.ts" }).click();
    await expect(selected).toHaveAttribute("data-path", "unrelated.ts");
    await selectSecond();
    await expect(selected).toHaveAttribute("data-path", "b.ts");
    await expect(page.getByTestId("file-version-document").filter({ visible: true })).toContainText('"b.ts"');
  } finally {
    releaseComparison();
    releaseSources();
  }
});
