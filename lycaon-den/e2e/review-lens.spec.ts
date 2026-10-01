import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import {
  activateProject,
  openProjectFilesFixture,
  webE2e,
} from "./helpers.ts";

webE2e("review pin keeps its name after reload", async ({ page, request }) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "review-pin-resume",
    name: "Review pin resume",
    seed: (root) => writeFileSync(path.join(root, "readme.md"), "# Pin fixture\n"),
  });
  await page.getByTestId("sidebar-scope-picker").click();
  const created = page.waitForResponse((response) =>
    response.url().endsWith(`/v1/projects/${project.id}/source/pins`) &&
    response.request().method() === "POST");
  await page.getByTestId("sidebar-scope-pin-current").click();
  const pin = await (await created).json() as { id: string; label: string };
  await expect(page.getByTestId("sidebar-scope-picker")).toHaveAttribute("aria-label", `Scope: ${pin.label}`);
  await page.reload();
  await activateProject(page, project.id);
  await page.getByTestId("project-files-entry").click();
  await expect(page.getByTestId("sidebar-scope-picker")).toHaveAttribute("aria-label", `Scope: ${pin.label}`);
  await page.getByTestId("sidebar-scope-picker").click();
  await page.getByTestId(`sidebar-scope-remove-pin-${pin.id}`).click();
  await expect(page.getByTestId("sidebar-scope-picker")).toHaveAttribute("aria-label", "Scope: New since you looked");
});

type HarnessWindow = Window & {
  __harness?: {
    openReviewLens(
      projectId: string,
      scope?: { kind: string },
    ): { ok: boolean; error?: string };
    click(testid: string): Promise<{ ok: boolean }>;
  };
};

async function captureSelectionTravel(page: Page, targetTestId: string) {
  return page.evaluate(async (testId) => {
    const target = document.querySelector<HTMLElement>(
      `[data-testid="${testId}"]`,
    );
    const thumb = document.querySelector<HTMLElement>(
      '[data-testid="files-pane-segment"] .den-browse-segment-thumb',
    );
    if (!target || !thumb) throw new Error("selection control unavailable");

    const startX = thumb.getBoundingClientRect().x;
    const positions: number[] = [];
    let observedAnimation = false;
    target.click();
    const startedAt = performance.now();
    await new Promise<void>((resolve) => {
      const sample = (now: number) => {
        positions.push(thumb.getBoundingClientRect().x);
        observedAnimation ||= thumb.getAnimations().some(
          (animation) => animation.playState === "running",
        );
        if (now - startedAt >= 260) {
          resolve();
          return;
        }
        requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
    return {
      startX,
      targetX: target.getBoundingClientRect().x,
      positions,
      observedAnimation,
    };
  }, targetTestId);
}

webE2e("review lens: Files|Review selection thumb travels", async ({
  page,
  request,
}) => {
  await openProjectFilesFixture(page, request, {
    prefix: "review",
    name: "Review",
    readyTestId: "files-pane-segment",
    seed: (root) => writeFileSync(path.join(root, "readme.md"), "# hello\n"),
  });

  const track = page.getByTestId("files-pane-segment");
  const files = page.getByTestId("files-pane-files");
  const review = page.getByTestId("files-pane-review");
  const thumb = track.locator(".den-browse-segment-thumb");
  await expect(thumb).toBeVisible();
  await expect(files).toHaveAttribute("aria-pressed", "true");
  expect(
    await page.evaluate(() =>
      matchMedia("(prefers-reduced-motion: reduce)").matches,
    ),
  ).toBe(false);

  const outbound = await captureSelectionTravel(page, "files-pane-review");
  await expect(review).toHaveAttribute("aria-pressed", "true");
  expect(outbound.observedAnimation).toBe(true);
  expect(
    outbound.positions.some(
      (x) => x > outbound.startX + 1 && x < outbound.targetX - 1,
    ),
  ).toBe(true);
  expect(outbound.positions[outbound.positions.length - 1]).toBeCloseTo(
    outbound.targetX,
    0,
  );

  const inbound = await captureSelectionTravel(page, "files-pane-files");
  await expect(files).toHaveAttribute("aria-pressed", "true");
  expect(inbound.observedAnimation).toBe(true);
  expect(
    inbound.positions.some(
      (x) => x < inbound.startX - 1 && x > inbound.targetX + 1,
    ),
  ).toBe(true);
  expect(inbound.positions[inbound.positions.length - 1]).toBeCloseTo(
    inbound.targetX,
    0,
  );
});

webE2e("review lens: Files|Review segment + empty working tray", async ({
  page,
  request,
}) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "review",
    name: "Review",
    readyTestId: "files-pane-segment",
    seed: (root) => {
      writeFileSync(path.join(root, "readme.md"), "# hello\n");
      mkdirSync(path.join(root, "src"), { recursive: true });
      writeFileSync(path.join(root, "src", "a.ts"), "export const a = 1;\n");
    },
  });
  const opened = await page.evaluate((projectId) => {
    const h = (window as unknown as HarnessWindow).__harness;
    if (!h?.openReviewLens) return { ok: false, error: "no harness hook" };
    return h.openReviewLens(projectId, { kind: "new" });
  }, project.id);
  expect(opened.ok).toBe(true);

  if (!(await page.getByTestId("files-pane-segment").isVisible().catch(() => false))) {
    await page.getByTestId("project-files-entry").click();
  }
  await expect(page.getByTestId("files-pane-segment")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("review-lens")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("review-lens-scope-header")).toContainText(
    "New since you looked",
    { timeout: 15_000 },
  );

  await page.evaluate(async () => {
    const h = (window as unknown as HarnessWindow).__harness;
    if (!h) throw new Error("no harness");
    await h.click("sidebar-scope-picker");
  });
  await expect(page.getByTestId("sidebar-scope-picker-menu")).toBeVisible({
    timeout: 15_000,
  });
  const menuLayout = await page
    .getByTestId("sidebar-scope-picker-menu")
    .evaluate((menu) => {
      const viewport = menu.querySelector<HTMLElement>(
        ".den-sidebar-scope-picker__menu-scroll > .den-scrollport__viewport",
      );
      return {
        clientWidth: viewport?.clientWidth ?? 0,
        scrollWidth: viewport?.scrollWidth ?? Infinity,
        overflowX: viewport ? getComputedStyle(viewport).overflowX : "",
        listIsScrollContent: viewport
          ?.querySelector(":scope > .den-sidebar-scope-picker__menu-list")
          ?.classList.contains("den-scrollport__content") ?? false,
      };
    });
  expect(menuLayout.overflowX).toBe("hidden");
  expect(menuLayout.scrollWidth).toBeLessThanOrEqual(menuLayout.clientWidth + 1);
  expect(menuLayout.listIsScrollContent).toBe(true);
});
