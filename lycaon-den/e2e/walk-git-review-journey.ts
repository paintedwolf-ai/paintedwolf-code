import { execFileSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdirSync, unlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiConfig, modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";
import type { ProjectSourceReadResponse, SourceWalkResponse, SourceWorkspace } from "../src/api/types.ts";

type TabFrameCapture = { stopped: boolean; frames: string[][] };
type CaptureWindow = Window & { tabReviewCapture?: TabFrameCapture };

function git(root: string, ...args: string[]): string {
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("GIT_")));
  return execFileSync("git", ["-C", root, "-c", "user.name=Review test", "-c", "user.email=review@example.test", ...args], {
    encoding: "utf8", env: { ...env, GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: "/dev/null", GIT_TERMINAL_PROMPT: "0" },
  }).trim();
}

export function registerGitReviewJourney(browserName: "chromium" | "webkit"): void {
  modelIndependentWebE2e.use({ browserName });
  modelIndependentWebE2e("walk Git review opens immutable committed files and returns to the group", async ({ page, request }, testInfo) => {
    const outputRoot = process.env.LYCAON_REVIEW_SCREENSHOT_DIR;
    const screenshotDir = outputRoot ? path.join(outputRoot, browserName) : undefined;
    if (screenshotDir) mkdirSync(screenshotDir, { recursive: true });
    const screenshotPath = (name: string) => screenshotDir ? path.join(screenshotDir, name) : testInfo.outputPath(name);
    const { project, root } = await openProjectFilesFixture(page, request, {
      prefix: "walk-git-review", name: "Git walk review", readyTestId: "files-pane-segment",
      seed: (dir) => {
        git(dir, "init", "-b", "main");
        mkdirSync(path.join(dir, "src"));
        writeFileSync(path.join(dir, "src", "app.ts"), "export const value = 1;\n");
        writeFileSync(path.join(dir, "removed.ts"), "export const removed = true;\n");
        git(dir, "add", "-A");
        git(dir, "commit", "-m", "Create the application");
      },
    });
    const { apiUrl, token } = apiConfig();
    const headers = { Authorization: `Bearer ${token}` };
    await expect.poll(async () => {
      const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/workspace`, { headers });
      expect(response.ok()).toBeTruthy();
      return ((await response.json()) as SourceWorkspace).inventory.complete;
    }, { timeout: 30_000 }).toBe(true);
    const sourceUrl = `${apiUrl}/v1/projects/${project.id}/source`;
    const baseline = await request.get(sourceUrl, { headers, params: { path: "src/app.ts", root_id: project.roots[0]!.id } });
    expect(baseline.ok(), await baseline.text()).toBeTruthy();
    const source = await baseline.json() as ProjectSourceReadResponse;
    const pinResponse = await request.post(`${apiUrl}/v1/projects/${project.id}/source/pins`, { headers, data: { label: "Before the commit" } });
    expect(pinResponse.ok(), await pinResponse.text()).toBeTruthy();
    const { id: pinId } = await pinResponse.json() as { id: string };
    const saved = await request.put(sourceUrl, { headers, data: {
      operation_id: randomUUID(), path: source.path, root_id: source.root_id,
      base_sha256: source.sha256, encoding: source.encoding, content: "export const value = 2;\n",
    } });
    expect(saved.ok(), await saved.text()).toBeTruthy();
    const readWalk = async (): Promise<SourceWalkResponse> => {
      const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/walk`, { headers, params: { baseline: `pin:${pinId}` } });
      expect(response.ok()).toBeTruthy();
      return response.json() as Promise<SourceWalkResponse>;
    };
    writeFileSync(path.join(root, "src", "new.ts"), "export const added = true;\n");
    unlinkSync(path.join(root, "removed.ts"));
    await expect.poll(async () => (await readWalk()).files.some((file) => file.path === "src/app.ts"), { timeout: 30_000 }).toBe(true);
    git(root, "add", "-A");
    git(root, "commit", "-m", "Update the application", "-m", "Keep the new behavior and remove the obsolete file.");
    const commit = git(root, "rev-parse", "HEAD");
    let walk: SourceWalkResponse | undefined;
    await expect.poll(async () => {
      walk = await readWalk();
      return walk.git_changes.some((change) => change.to_commit === commit);
    }, { timeout: 30_000 }).toBe(true);
    const change = walk!.git_changes.find((entry) => entry.to_commit === commit)!;
    await page.evaluate(async ({ projectId, rootId }) => {
      const url = "/src/files/documents/project-files-buffers.ts";
      const buffers = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/project-files-buffers.ts");
      buffers.openFilesBuffer(projectId, { rootId, rootLabel: "Project root", path: "src/app.ts", intent: "transient" });
    }, { projectId: project.id, rootId: project.roots[0]!.id });
    const currentDocument = page.getByTestId("files-editor-host").filter({ visible: true });
    await expect(currentDocument).toContainText("export const value = 2;");
    await page.getByTestId("files-editor-copy").click({ button: "right" });
    await expect(page.getByRole("menu")).toBeVisible();
    let releaseReview!: () => void;
    const reviewGate = new Promise<void>((resolve) => { releaseReview = resolve; });
    let requestedReview = false;
    await page.route("**/source/git-changes/**/review**", async (route) => {
      requestedReview = true;
      await reviewGate;
      await route.continue();
    });
    await page.evaluate(() => {
      const capture: TabFrameCapture = { stopped: false, frames: [] };
      (window as CaptureWindow).tabReviewCapture = capture;
      const sample = () => {
        if (capture.stopped) return;
        const visible = [...window.document.querySelectorAll<HTMLElement>(".den-files-pane-presentation__content > .den-files-tab-deck > .den-resident-surface")]
          .filter((surface) => {
            const style = getComputedStyle(surface);
            const bounds = surface.getBoundingClientRect();
            return style.display !== "none" && style.visibility !== "hidden" &&
              style.opacity !== "0" && bounds.width > 0 && bounds.height > 0;
          });
        capture.frames.push(visible.map((surface) => `${surface.dataset.presentation}:${getComputedStyle(surface).opacity}`));
        requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
    await page.evaluate(async ({ projectId, change }) => {
      const url = "/src/files/documents/project-files-buffers.ts";
      const buffers = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/project-files-buffers.ts");
      buffers.openFilesBuffer(projectId, {
        kind: "walk", name: "Git commit · on main", rootId: "", rootLabel: "", path: "", intent: "transient",
        walkStep: { kind: "git", key: `git:${change.id}`, change, ordinal: change.ordinal, effects: [], toolCallId: null, label: "git commit" },
      });
    }, { projectId: project.id, change });
    await expect.poll(() => requestedReview).toBe(true);
    await expect(page.getByRole("menu")).not.toBeVisible();
    await expect(currentDocument).toBeVisible();
    await expect(currentDocument).toContainText("export const value = 2;");
    await expect(page.getByTestId("walk-git-review")).not.toBeVisible();
    await expect(page.getByTestId("files-pane-presentation")).toHaveAttribute("aria-busy", "true");
    const deckBounds = await page.locator(".den-files-pane-presentation__content > .den-files-tab-deck").boundingBox();
    expect(deckBounds).not.toBeNull();
    const corner = { x: Math.floor(deckBounds!.x + deckBounds!.width - 24), y: Math.floor(deckBounds!.y + deckBounds!.height - 24), width: 8, height: 8 };
    const retainedPaint = await page.screenshot({ clip: corner });
    await page.locator(".den-files-pane-presentation__content > .den-files-tab-deck > .den-resident-surface--pending").evaluate((surface) => {
      const marker = window.document.createElement("div");
      marker.dataset.testVisibilityOverride = "true";
      marker.style.cssText = "position: absolute; inset: 0; z-index: 9999; visibility: visible; background: #00ff00; pointer-events: none";
      surface.appendChild(marker);
    });
    const preparationPaint = await page.screenshot({ clip: corner });
    expect(preparationPaint.equals(retainedPaint), "A preparing tab must hide descendants that override visibility").toBe(true);
    await page.locator("[data-test-visibility-override]").evaluate((marker) => marker.remove());
    await page.screenshot({ path: screenshotPath("git-commit-preparing.png"), fullPage: true });
    releaseReview();
    await expect(page.getByTestId("walk-git-review")).toBeVisible();
    await expect(page.getByTestId("git-review-count")).toContainText("3 files committed");
    await page.getByText("Full commit message", { exact: true }).click();
    await expect(page.getByTestId("walk-git-review")).toContainText("Keep the new behavior");
    await expect(page.locator(".den-files-pane-presentation__content")).toHaveCSS("opacity", "1");
    await page.screenshot({ path: screenshotPath("git-commit-group.png"), fullPage: true });
    writeFileSync(path.join(root, "src", "app.ts"), "export const value = 99;\n");
    await page.getByRole("button", { name: "Review src/app.ts", exact: true }).click();
    const document = page.getByTestId("file-version-document").filter({ visible: true });
    await expect(document).toContainText("export const value = 2;");
    await expect(document).not.toContainText("export const value = 99;");
    await expect(document.locator(".cm-content")).toHaveAttribute("contenteditable", "false");
    await expect(page.locator(".den-files-pane-presentation__content")).toHaveCSS("opacity", "1");
    await page.screenshot({ path: screenshotPath("git-commit-file.png"), fullPage: true });
    await page.getByRole("button", { name: "Back to Git review", exact: true }).click();
    await expect(page.getByTestId("git-review-count")).toContainText("3 files committed");
    await expect(page.getByTestId("walk-git-review").locator("details.den-git-review__message")).toHaveAttribute("open", "");
    await page.getByRole("button", { name: "Review removed.ts", exact: true }).click();
    await expect(page.getByTestId("git-review-preview").filter({ visible: true })).toContainText("removed.ts");
    await expect(page.getByTestId("file-version-document").filter({ visible: true })).toContainText("export const removed = true;");
    const frames = await page.evaluate(() => {
      const capture = (window as CaptureWindow).tabReviewCapture!;
      capture.stopped = true;
      return capture.frames;
    });
    expect(frames.length).toBeGreaterThan(0);
    expect(frames.every((frame) => frame.length === 1 && frame[0] === "published:1"), JSON.stringify(frames)).toBe(true);
  });
}
