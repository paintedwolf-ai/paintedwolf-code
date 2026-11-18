import { expect, test } from "@playwright/test";
import type { ArtifactListResponse } from "../src/api/types.ts";
import {
  activateProject,
  apiFindHarnessProject,
  apiJson,
  apiPostPrompt,
  apiUploadAttachment,
  CONTEXT_NAV_WITH,
  openNewChatSession,
  seedAppState,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

/** Exact base64 of visual.TestPNG1x1Bytes. */
const PNG_B64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVQI12P4z8AAAAMBAQAY3Y20AAAAAElFTkSuQmCC";
const PNG_SECOND_B64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNQmHAHAAJgAY2WsQ4zAAAAAElFTkSuQmCC";

// Manual responses keep the seeded image turn open.
webE2e(
  "den:harness — Artifacts gallery shows tiles, source badge, and lightbox",
  async ({ page, request }, testInfo) => {
    test.skip(
      process.env.LYCAON_LLM_MANUAL !== "1",
      "set LYCAON_LLM_MANUAL=1 to seed a durable user artifact",
    );
    test.setTimeout(180_000);

    await seedAppState(page, {
      contextNav: CONTEXT_NAV_WITH("artifacts"),
    });
    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
    try {
      const upload = await apiUploadAttachment(
        request,
        project.id,
        "gallery-fixture.png",
        "image/png",
        Buffer.from(PNG_B64, "base64"),
      );
      const promptRes = await apiPostPrompt(
        request,
        sessionId,
        {
          text: "gallery fixture image",
          attachments: [{ blob_id: upload.blob_id }],
        },
      );
      expect(promptRes.ok(), await promptRes.text()).toBeTruthy();

      await expect
        .poll(
          async () => (await apiJson<ArtifactListResponse>(request, "GET", `/v1/projects/${project.id}/artifacts`)).artifacts.length,
          { timeout: 60_000 },
        )
        .toBeGreaterThan(0);

      const pending = await apiJson<{ pending: boolean; id: string }>(
        request, "GET", `/harness/llm/pending?wait=30000&session_id=${sessionId}`,
      );
      expect(pending.pending).toBe(true);
      await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Got the gallery fixture." });
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Harness reply." });
    }

    await page.getByTestId("project-artifacts-entry").click();
    await expect(page.getByTestId("project-artifacts-view")).toBeVisible({
      timeout: 60_000,
    });
    await expect(page.getByTestId("project-artifacts-gallery")).toBeVisible({
      timeout: 60_000,
    });

    const tile = page.getByTestId("project-artifact-tile").first();
    await expect(tile).toBeVisible();
    const firstArtifactId = await tile.getAttribute("data-artifact-id");
    expect(firstArtifactId).toBeTruthy();
    await expect(
      tile.getByTestId("project-artifact-source-badge"),
    ).toHaveText(/User|Render|Capture/);

    const enlarge = tile.getByRole("button").first();
    await expect(enlarge).toBeEnabled({ timeout: 60_000 });
    await enlarge.click();
    const lightbox = page.getByTestId("project-artifacts-lightbox");
    await expect(lightbox).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(lightbox).toBeHidden();

    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
    try {
      const upload = await apiUploadAttachment(
        request,
        project.id,
        "gallery-live-second.png",
        "image/png",
        Buffer.from(PNG_SECOND_B64, "base64"),
      );
      const promptRes = await apiPostPrompt(request, sessionId, {
        text: "second live gallery image",
        attachments: [{ blob_id: upload.blob_id }],
      });
      expect(promptRes.ok(), await promptRes.text()).toBeTruthy();

      const pending = await apiJson<{ pending: boolean; id: string }>(
        request,
        "GET",
        `/harness/llm/pending?wait=30000&session_id=${sessionId}`,
      );
      expect(pending.pending).toBe(true);
      await apiJson(request, "POST", "/harness/llm/respond", {
        id: pending.id,
        content: "Got the second gallery fixture.",
      });
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", {
        enabled: true,
        text: "Harness reply.",
      });
    }

    const tiles = page.getByTestId("project-artifact-tile");
    await expect(tiles).toHaveCount(2, { timeout: 60_000 });
    await expect(tiles.nth(1)).toHaveAttribute("data-artifact-id", firstArtifactId!);
    const secondArtifactId = await tiles.nth(0).getAttribute("data-artifact-id");
    expect(secondArtifactId).toBeTruthy();
    expect(secondArtifactId).not.toBe(firstArtifactId);

    const newestEnlarge = tiles.nth(0).getByRole("button").first();
    const olderEnlarge = tiles.nth(1).getByRole("button").first();
    await expect(newestEnlarge).toBeEnabled({ timeout: 60_000 });
    await expect(olderEnlarge).toBeEnabled({ timeout: 60_000 });
    await expect(page.getByTestId("project-artifact-load-error")).toHaveCount(0);

    // Tiles share a row, and each preview is letterboxed inside its thumbnail frame.
    const [newest, older] = [await tiles.nth(0).boundingBox(), await tiles.nth(1).boundingBox()];
    expect(Math.abs(newest!.y - older!.y)).toBeLessThanOrEqual(1);
    expect(older!.x).toBeGreaterThan(newest!.x);
    const frame = await tiles.nth(0).locator(".den-transcript-visual__frame").boundingBox();
    expect(frame!.width / frame!.height).toBeCloseTo(1.6, 1);
    const preview = tiles.nth(0).locator("img.den-transcript-visual__img");
    await expect(preview).toHaveCSS("object-fit", "contain");
    const previewBox = await preview.boundingBox();
    expect(previewBox!.height).toBeLessThanOrEqual(frame!.height + 1);
    expect(previewBox!.width).toBeLessThanOrEqual(frame!.width + 1);

    // A short window scrolls the gallery inside the stage, not the page.
    const viewport = page.viewportSize()!;
    await page.setViewportSize({ width: 900, height: 360 });
    expect(await page.evaluate(() => document.scrollingElement!.scrollHeight <= window.innerHeight + 1)).toBe(true);
    const grid = await page.locator(".den-artifacts-grid").boundingBox();
    expect(grid!.y + grid!.height).toBeLessThanOrEqual(360 + 1);
    await tiles.nth(1).scrollIntoViewIfNeeded();
    await expect(tiles.nth(1)).toBeInViewport();
    await page.setViewportSize(viewport);

    await page.screenshot({
      path:
        process.env.LYCAON_REVIEW_SCREENSHOT ??
        testInfo.outputPath("artifacts-stage-live.png"),
      animations: "disabled",
    });

    await newestEnlarge.click();
    await expect(lightbox).toBeVisible();
    await expect(lightbox.locator("img")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(lightbox).toBeHidden();

    await olderEnlarge.click();
    await expect(lightbox).toBeVisible();
    await expect(lightbox.locator("img")).toBeVisible();
  },
);
