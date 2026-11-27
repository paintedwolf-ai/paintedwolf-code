import { expect, type Locator } from "@playwright/test";
import type { ArtifactListResponse, Message } from "../src/api/types.ts";
import {
  activateProject,
  apiJson,
  apiPostPrompt,
  bootstrapChatSession,
  CONTEXT_NAV_WITH,
  expectShellReady,
  liveChatStage,
  openSessionRow,
  settledChatSessionId,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import { manualHarnessTools, shellWord } from "./manual-harness-tools.ts";

type PendingTurn = { pending: boolean; id: string; session_id: string };

async function expandDisclosure(container: Locator) {
  await expect(container).toBeVisible({ timeout: 30_000 });
  if ((await container.getAttribute("data-expanded")) !== "true") {
    await container.locator(":scope > .den-transcript-disclosure-header").click();
  }
  await expect(container).toHaveAttribute("data-expanded", "true");
}

webE2e("browser-obligation: build receipt leaves browser obligation open until capture_page retains UI artifact provenance", async ({ page, request }, testInfo) => {
  webE2e.setTimeout(180_000);

  const project = await bootstrapChatSession(page, request, {
    contextNav: CONTEXT_NAV_WITH("artifacts"),
  });
  const sessionId = await settledChatSessionId(page);
  const siteDir = `browser-obligation-${crypto.randomUUID().slice(0, 8)}`;

  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });

  try {
    const prompt = await apiPostPrompt(request, sessionId, {
      text: "Build index.html with an interactive Ping button and verify it in the browser.",
    });
    expect(prompt.ok(), await prompt.text()).toBe(true);

    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("update_progress");
    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [ ] Build index.html and verify via capture_page\n",
    }));

    // Phase 1: build index.html via portable command (external_exec / completed receipt).
    const buildHtml = [
      "<!doctype html><html><body>",
      "<button data-testid=\"ping\" onclick=\"document.getElementById('s').textContent='verified'\">Ping</button>",
      "<p id=\"s\" data-testid=\"status\">ready</p>",
      "</body></html>",
    ].join("");
    const buildCmd = `mkdir -p ${siteDir} && printf '%s\\n' ${shellWord(buildHtml)} > ${siteDir}/index.html && test -s ${siteDir}/index.html`;
    const buildCallId = await driver.invoke("command", { command: buildCmd });
    await driver.completed(buildCallId);

    // Verify build receipt completed while zero browser capture artifacts exist for this session yet.
    const midTranscript = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`);
    const buildResult = midTranscript.messages.find((m) => m.tool_result?.tool_call_id === buildCallId)?.tool_result;
    expect(buildResult?.outcome).toBe("completed");
    expect(buildResult?.command_program?.state).toBe("completed");

    const beforeArtifacts = await apiJson<ArtifactListResponse>(request, "GET", `/v1/projects/${project.id}/artifacts`);
    const capturesBefore = beforeArtifacts.artifacts.filter((a) => a.source === "capture" && a.session_id === sessionId);
    expect(capturesBefore).toHaveLength(0);

    // Load structured browser operation capture_page to satisfy the open browser obligation.
    await driver.loadTools("capture_page");

    // Phase 2: drive actual UI activity via capture_page and retain visual artifact provenance.
    const caption = "Browser obligation UI check";
    const captureCallId = await driver.invoke("capture_page", {
      project_dir: siteDir,
      caption,
      actions: [
        { type: "click", testid: "ping" },
        { type: "wait_for", text: "verified" },
      ],
    });
    const captureOutput = await driver.completed(captureCallId);
    expect(captureOutput).toBeTruthy();

    // Mark the progress item complete so closeout is unblocked after browser verification.
    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [x] Build index.html and verify via capture_page\n",
    }));

    const pending = await apiJson<PendingTurn>(request, "GET", `/harness/llm/pending?wait=30000&session_id=${sessionId}`);
    expect(pending.pending).toBe(true);
    await apiJson(request, "POST", "/harness/llm/respond", {
      id: pending.id,
      content: "Built index.html and verified the Ping interaction via capture_page.",
    });
    await expect.poll(
      async () => (await apiJson<{ status: string }>(request, "GET", `/v1/sessions/${sessionId}`)).status,
      { timeout: 30_000 },
    ).toBe("idle");
    const backToChat = page.getByRole("button", { name: "Back to chat" });
    if (await backToChat.isVisible().catch(() => false)) {
      await backToChat.click();
    }
    await expect(liveChatStage(page).getByTestId("transcript-article-assistant").last()).toContainText(
      "Built index.html and verified the Ping interaction via capture_page.",
      { timeout: 30_000 },
    );

    // Verify capture_page renders as a settled activity card in Den chat.
    const span = liveChatStage(page).getByTestId("activity-span-card").last();
    await expandDisclosure(span);
    const captureCard = liveChatStage(page).locator('[data-tool="capture_page"]').last();
    await expect(captureCard).toBeAttached({ timeout: 30_000 });
    await expandDisclosure(captureCard);
    await expect(captureCard).toHaveAttribute("data-status", "done", { timeout: 15_000 });

    // Verify retained artifact metadata and provenance via project artifacts API.
    let capturedArtifact: ArtifactListResponse["artifacts"][number] | undefined;
    await expect.poll(async () => {
      const list = await apiJson<ArtifactListResponse>(request, "GET", `/v1/projects/${project.id}/artifacts`);
      capturedArtifact = list.artifacts.find(
        (item) => item.source === "capture" && item.session_id === sessionId && item.tool_call_id === captureCallId,
      );
      return Boolean(capturedArtifact);
    }, { timeout: 30_000 }).toBe(true);

    expect(capturedArtifact?.mime).toMatch(/^image\//);
    expect(capturedArtifact?.caption).toBe(caption);
    expect(capturedArtifact?.content_hash).toMatch(/^[a-f0-9]{64}$/);
    const artifactId = capturedArtifact!.id;

    // Verify retained capture artifact renders in Den's Project Artifacts Gallery with Capture badge.
    await page.getByTestId("project-artifacts-entry").click();
    await expect(page.getByTestId("project-artifacts-view")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("project-artifacts-gallery")).toBeVisible({ timeout: 30_000 });
    const tile = page.locator(`[data-testid="project-artifact-tile"][data-artifact-id="${artifactId}"]`);
    await expect(tile).toBeVisible({ timeout: 30_000 });
    await expect(tile.getByTestId("project-artifact-source-badge")).toHaveText("Capture");

    // Reload Den and confirm both the capture_page activity and retained artifact survive reload.
    await page.reload();
    await waitHarnessConnected(page);
    await expectShellReady(page);
    await activateProject(page, project.id);
    await openSessionRow(page, sessionId);

    const reloadedSpan = liveChatStage(page).getByTestId("activity-span-card").last();
    await expandDisclosure(reloadedSpan);
    await expect(liveChatStage(page).locator('[data-tool="capture_page"]').last()).toBeAttached({ timeout: 30_000 });

    const afterReloadArtifacts = await apiJson<ArtifactListResponse>(request, "GET", `/v1/projects/${project.id}/artifacts`);
    const persisted = afterReloadArtifacts.artifacts.find((item) => item.id === artifactId);
    expect(persisted?.source).toBe("capture");
    expect(persisted?.content_hash).toBe(capturedArtifact?.content_hash);

    console.log("BROWSER_OBLIGATION_EVIDENCE:" + JSON.stringify({
      case: "browser-obligation",
      projectId: project.id,
      sessionId,
      buildCallId,
      buildReceiptState: buildResult?.command_program?.state ?? null,
      capturesBeforeCapturePage: capturesBefore.length,
      captureCallId,
      artifactId,
      artifactSource: persisted?.source ?? null,
      artifactMime: persisted?.mime ?? null,
      artifactContentHash: persisted?.content_hash ?? null,
      artifactToolCallId: persisted?.tool_call_id ?? null,
      survivedReload: true,
    }));
  } catch (error) {
    const transcript = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`).catch(() => undefined);
    const artifacts = await apiJson<ArtifactListResponse>(request, "GET", `/v1/projects/${project.id}/artifacts`).catch(() => undefined);
    await testInfo.attach("browser-obligation-failure", {
      body: JSON.stringify({ transcript, artifacts }, null, 2),
      contentType: "application/json",
    });
    throw error;
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Done." }).catch(() => undefined);
    const pending = await apiJson<PendingTurn>(request, "GET", "/harness/llm/pending").catch(() => undefined);
    if (pending?.pending) {
      await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Done." }).catch(() => undefined);
    }
  }
});
