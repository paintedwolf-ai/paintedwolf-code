import { expect, test, type Page } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  apiPostPrompt,
  apiUploadAttachment,
  liveChatStage,
  openNewChatSession,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

/** Exact base64 of visual.TestPNG1x1Bytes. */
const PNG_B64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVQI12P4z8AAAAMBAQAY3Y20AAAAAElFTkSuQmCC";

type Harness = {
  llm: {
    manual(): Promise<unknown>;
    pending(waitMs?: number): Promise<{ pending?: boolean }>;
    respond(reply: { text?: string }): Promise<unknown>;
  };
  waitForTestid(testid: string, timeoutMs?: number): Promise<{ ok: boolean }>;
  waitForIdle(timeoutMs?: number): Promise<{ ok: boolean }>;
  goHome(): Promise<{ ok: boolean }>;
};

async function enableManualLLM(page: Page) {
  await page.evaluate(async () => {
    const h = (window as unknown as { __harness: Harness }).__harness;
    await h.llm.manual();
  });
}

/** Leave chat and resume the same session so GET /messages hydrates API-written rows. */
async function reconcileSessionInPlace(
  page: Page,
  projectId: string,
  sessionId: string,
) {
  await page.evaluate(async () => {
    const h = (window as unknown as { __harness: Harness }).__harness;
    await h.goHome();
  });
  await activateProject(page, projectId);
  const sessionRow = page
    .getByTestId("focused-session-list")
    .locator(`button[data-session-id="${sessionId}"]`);
  await expect(sessionRow).toBeVisible({ timeout: 60_000 });
  await sessionRow.click();
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
    timeout: 90_000,
  });
  await expect(liveChatStage(page).getByTestId("chat-stream")).toHaveAttribute(
    "data-session-id",
    sessionId,
  );
}

/**
 * Re-present one artifact → one full <img> + a reference chip.
 * Requires LYCAON_LLM_MANUAL=1 on the harness stack.
 */
webE2e(
  "den:harness — re-presenting one artifact yields one img + a chip",
  async ({ page, request }) => {
    test.skip(
      process.env.LYCAON_LLM_MANUAL !== "1",
      "set LYCAON_LLM_MANUAL=1 for scripted closeout presents",
    );
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);

    const sessionId = await openNewChatSession(page);

    await enableManualLLM(page);

    // Turn 1 — user image attachment (canonical full render).
    let artifactId = "";
    const upload = await apiUploadAttachment(
      request,
      project.id,
      "fixture.png",
      "image/png",
      Buffer.from(PNG_B64, "base64"),
    );
    const promptRes = await apiPostPrompt(
      request,
      sessionId,
      {
        text: "here is a fixture image",
        attachments: [{ blob_id: upload.blob_id }],
      },
    );
    expect(promptRes.ok(), await promptRes.text()).toBeTruthy();

    await expect
      .poll(
        async () => {
          const pageRes = await request.get(
            `${apiUrl}/v1/sessions/${sessionId}/messages`,
            { headers: auth },
          );
          if (!pageRes.ok()) return "";
          const body = (await pageRes.json()) as {
            messages?: Array<{ artifact_ids?: string[] }>;
          };
          artifactId = (body.messages ?? [])
            .flatMap((m) => m.artifact_ids ?? [])
            .map((id) => id.trim())
            .find(Boolean) ?? "";
          return artifactId;
        },
        { timeout: 60_000 },
      )
      .toBeTruthy();

    await reconcileSessionInPlace(page, project.id, sessionId);
    await enableManualLLM(page);

    await expect(page.getByText("here is a fixture image")).toBeVisible({
      timeout: 60_000,
    });
    await expect(page.getByTestId("visual-present-block")).toBeVisible({
      timeout: 60_000,
    });

    await page.evaluate(async () => {
      const h = (window as unknown as { __harness: Harness }).__harness;
      await h.llm.pending(60_000);
      await h.llm.respond({
        text: JSON.stringify({
          synthesis: "Got the fixture.",
          cited_evidence: [],
        }),
      });
    });

    // Turn 2 — assistant closeout re-presents the same artifact id.
    await enableManualLLM(page);
    const reprompt = await apiPostPrompt(
      request,
      sessionId,
      { text: "present that fixture again" },
    );
    expect(reprompt.ok(), await reprompt.text()).toBeTruthy();

    await page.evaluate(
      async ({ id }) => {
        const h = (window as unknown as { __harness: Harness }).__harness;
        await h.llm.pending(60_000);
        await h.llm.respond({
          text: JSON.stringify({
            synthesis: "Same fixture again.",
            cited_evidence: [],
            artifact_ids: [id],
          }),
        });
      },
      { id: artifactId },
    );

    await expect
      .poll(
        async () => {
          const pageRes = await request.get(
            `${apiUrl}/v1/sessions/${sessionId}/messages`,
            { headers: auth },
          );
          if (!pageRes.ok()) return 0;
          const body = (await pageRes.json()) as {
            messages?: Array<{ role?: string; artifact_ids?: string[] }>;
          };
          return (body.messages ?? []).filter(
            (m) =>
              m.role === "assistant" &&
              (m.artifact_ids ?? []).includes(artifactId),
          ).length;
        },
        { timeout: 60_000 },
      )
      .toBeGreaterThan(0);

    await reconcileSessionInPlace(page, project.id, sessionId);

    const full = page.locator(
      `[data-testid="transcript-visual-artifact"][data-artifact-id="${artifactId}"]`,
    );
    const chips = page.locator(
      `[data-testid="artifact-reference-chip"][data-artifact-id="${artifactId}"]`,
    );

    await expect.poll(async () => full.count(), { timeout: 60_000 }).toBe(1);
    await expect.poll(async () => chips.count(), { timeout: 60_000 }).toBe(1);
    await expect
      .poll(async () => page.locator("img.den-transcript-visual__img").count(), {
        timeout: 60_000,
      })
      .toBe(1);

    const removed = await request.delete(
      `${apiUrl}/v1/projects/${project.id}/artifacts/${artifactId}`,
      { headers: auth },
    );
    expect(removed.ok(), await removed.text()).toBeTruthy();
    await expect(full).toHaveCount(0);
    await expect(chips).toBeDisabled();
    await expect(page.getByText("This artifact was deleted.", { exact: true })).toBeVisible();
    await expect(page.locator("img.den-transcript-visual__img")).toHaveCount(0);

    await page.reload();
    await waitHarnessConnected(page);
    await expect(page.getByText("This artifact was deleted.", { exact: true })).toBeVisible();
    await expect(chips).toBeDisabled();
    await expect(page.locator("img.den-transcript-visual__img")).toHaveCount(0);

  },
);
