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

// Manual responses keep the seeded image turn open.
webE2e(
  "den:harness — Artifacts gallery shows tiles, source badge, and lightbox",
  async ({ page, request }) => {
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
    await expect(
      tile.getByTestId("project-artifact-source-badge"),
    ).toHaveText(/User|Render|Capture/);

    const enlarge = tile.getByRole("button").first();
    await expect(enlarge).toBeEnabled({ timeout: 60_000 });
    await enlarge.click();
    await expect(page.getByTestId("project-artifacts-lightbox")).toBeVisible();
  },
);
