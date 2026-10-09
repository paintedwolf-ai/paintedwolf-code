import { expect } from "@playwright/test";
import {
  apiConfig,
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "../helpers.ts";
import {
  startTailBoundaryTrace,
  stopTailBoundaryTrace,
  setTailBoundaryTracePhase,
  transcriptTailGap,
  expectTranscriptTailBounded,
  placeTranscriptAtTail,
  placeTranscriptNearTail,
} from "./tail-helpers.ts";

webE2e("viewport contractions never leave the transcript below its latest row", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  await page.setViewportSize({ width: 720, height: 620 });
  const sessionId = await settledChatSessionId(page);

  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      ...Array.from({ length: 24 }, (_, index) => ({
        id: `00000000-0000-4000-a000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Resize runway ${index + 1}: ${"wrapping content ".repeat(28)}`,
        created_at: `2026-08-23T16:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ]),
  );

  await placeTranscriptAtTail(page);

  for (const size of [
    { width: 1_440, height: 620 },
    { width: 820, height: 760 },
    { width: 1_280, height: 840 },
    { width: 720, height: 620 },
  ]) {
    await page.setViewportSize(size);
    await expectTranscriptTailBounded(page);
  }

  await placeTranscriptNearTail(page);
  for (const size of [
    { width: 1_520, height: 540 },
    { width: 680, height: 900 },
    { width: 1_360, height: 700 },
    { width: 760, height: 640 },
    { width: 1_440, height: 820 },
  ]) {
    await page.setViewportSize(size);
  }
  await expectTranscriptTailBounded(page);
});

webE2e("composer asks and approvals cannot publish space below the transcript", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 18 }, (_, index) => ({
        id: `00000000-0000-4000-b000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Composer runway ${index + 1}: ${"content ".repeat(18)}`,
        created_at: `2026-08-23T17:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );
  await expect(
    liveChatStage(page).locator(".transcript-viewport-row").getByText("Composer runway 18:", { exact: false }),
  ).toBeAttached({ timeout: 30_000 });
  await placeTranscriptAtTail(page);

  const { apiUrl, token } = apiConfig();
  const headers = {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  };
  const stage = liveChatStage(page);
  await startTailBoundaryTrace(page);

  // Textarea resizing leaves optional-card presence unchanged.
  const composer = stage.getByTestId("chat-composer");
  const stream = stage
    .locator(".den-chat-stream")
    .first();
  const composerScrollTop = await stream.evaluate((element) => element.scrollTop);
  await setTailBoundaryTracePhase(page, "composer growth");
  await composer.fill(
    Array.from({ length: 14 }, (_, index) =>
      `Composer growth line ${index + 1}: ${"wrapping text ".repeat(8)}`,
    ).join("\n"),
  );
  // Following keeps the latest row in view above the growing composer.
  await expect
    .poll(() => stream.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(composerScrollTop);
  await expect.poll(async () => {
    const state = await transcriptTailGap(page);
    return Math.abs(state.gap - state.clearance) <= 2
      ? "at tail"
      : JSON.stringify(state);
  }).toBe("at tail");
  await setTailBoundaryTracePhase(page, "composer contraction");
  await composer.fill("");
  await expectTranscriptTailBounded(page);

  await setTailBoundaryTracePhase(page, "ask dock");
  const ask = await request.post(`${apiUrl}/harness/ask_user`, {
    headers,
    data: {
      session_id: sessionId,
      prompt: `Choose a direction. ${"More context. ".repeat(20)}`,
    },
  });
  expect(ask.ok(), await ask.text()).toBeTruthy();
  const askDock = stage.getByTestId("ask-user-dock");
  await expect(askDock).toBeVisible({ timeout: 30_000 });
  await askDock.getByTestId("ask-user-dock-minimize").click();
  await expect(askDock).toHaveAttribute("data-minimized", "");
  await askDock.getByTestId("ask-user-dock-expand").click();
  await expect(askDock).not.toHaveAttribute("data-minimized", "");

  await setTailBoundaryTracePhase(page, "approval details");
  const approval = await request.post(
    `${apiUrl}/harness/checkpoints/tool_approval`,
    {
      headers,
      data: {
        session_id: sessionId,
        command: `printf '%s' '${"approval detail ".repeat(12)}'`,
      },
    },
  );
  expect(approval.ok(), await approval.text()).toBeTruthy();
  const card = stage.getByTestId("tool-approval-card");
  await expect(card).toBeVisible({ timeout: 30_000 });
  await card.getByTestId("approval-card-minimize").click();
  await card.getByTestId("approval-card-expand").click();

  const resolveApproval = async (checkpointId: string) => {
    await setTailBoundaryTracePhase(page, "approval resolution");
    const pending = stage.locator(
      `[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`,
    );
    const resolved = page.waitForResponse(
      (response) =>
        response.url().includes(`/checkpoints/${checkpointId}`) &&
        response.request().method() === "POST",
      { timeout: 30_000 },
    );
    await pending.getByTestId("approval-approve-primary").click();
    expect((await resolved).ok()).toBeTruthy();
    await expect(pending).toHaveCount(0, { timeout: 30_000 });
  };

  const firstCheckpointId = await card.getAttribute("data-checkpoint-id");
  expect(firstCheckpointId).toBeTruthy();
  if (!firstCheckpointId) throw new Error("approval checkpoint id is missing");
  await resolveApproval(firstCheckpointId);
  await expect(stage.getByTestId("composer-chrome-slot-checkpoint")).toHaveCount(0);
  await expectTranscriptTailBounded(page);

  const injectApproval = async (command: string) => {
    const response = await request.post(
      `${apiUrl}/harness/checkpoints/tool_approval`,
      { headers, data: { session_id: sessionId, command } },
    );
    const body = (await response.json()) as { checkpoint_id?: string };
    expect(response.ok(), JSON.stringify(body)).toBeTruthy();
    expect(body.checkpoint_id).toBeTruthy();
    if (!body.checkpoint_id) throw new Error("injected checkpoint id is missing");
    return body.checkpoint_id;
  };

  const tallCheckpointId = await injectApproval(
    `printf '%s' '${"large replacement approval ".repeat(28)}'`,
  );
  const shortCheckpointId = await injectApproval("pwd");
  const tallCard = stage.locator(
    `[data-testid="tool-approval-card"][data-checkpoint-id="${tallCheckpointId}"]`,
  );
  await expect(tallCard).toBeVisible({ timeout: 30_000 });
  await expect(stage.getByTestId("checkpoint-queue-row")).toHaveCount(1);
  await expect(tallCard.getByRole("button", {name:"Approval details in Files",exact:true})).toBeVisible();

  const shortCard = stage.locator(
    `[data-testid="tool-approval-card"][data-checkpoint-id="${shortCheckpointId}"]`,
  );
  // Queue replacement keeps slot presence stable.
  await setTailBoundaryTracePhase(page, "approval replacement");
  await stage
    .locator(
      `[data-testid="checkpoint-queue-row"][data-checkpoint-id="${shortCheckpointId}"]`,
    )
    .click();
  await expect(shortCard).toBeVisible();
  await expectTranscriptTailBounded(page);
  await stage
    .locator(
      `[data-testid="checkpoint-queue-row"][data-checkpoint-id="${tallCheckpointId}"]`,
    )
    .click();
  await expect(tallCard).toBeVisible();
  await expectTranscriptTailBounded(page);
  await resolveApproval(tallCheckpointId);

  await expect(shortCard).toBeVisible({ timeout: 30_000 });
  await expectTranscriptTailBounded(page);
  await resolveApproval(shortCheckpointId);
  await expect(stage.getByTestId("composer-chrome-slot-checkpoint")).toHaveCount(0);

  await expectTranscriptTailBounded(page);
  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  expect(Math.max(...trace.overflow), JSON.stringify(trace.samples.filter((sample) => sample.overflow > 2)))
    .toBeLessThanOrEqual(2);
});

webE2e("approval resolution cannot leave the viewport below the transcript", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 20 }, (_, index) => ({
        id: `00000000-0000-4000-c000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Approval removal runway ${index + 1}: ${"content ".repeat(20)}`,
        created_at: `2026-08-23T18:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );
  await expect(
    liveChatStage(page).locator(".transcript-viewport-row").getByText("Approval removal runway 20:", { exact: false }),
  ).toBeAttached({ timeout: 30_000 });
  await placeTranscriptAtTail(page);

  const { apiUrl, token } = apiConfig();
  const headers = {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  };
  const injected = await request.post(
    `${apiUrl}/harness/checkpoints/tool_approval`,
    {
      headers,
      data: {
        session_id: sessionId,
        command: `printf '%s' '${"approval removal detail ".repeat(36)}'`,
      },
    },
  );
  const injectedBody = (await injected.json()) as { checkpoint_id?: string };
  expect(injected.ok(), JSON.stringify(injectedBody)).toBeTruthy();
  const checkpointId = injectedBody.checkpoint_id;
  if (!checkpointId) throw new Error("injected checkpoint id is missing");

  const stage = liveChatStage(page);
  const card = stage.locator(
    `[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`,
  );
  await expect(card).toBeVisible({ timeout: 30_000 });
  await expect(card.getByRole("button", {name:"Approval details in Files",exact:true})).toBeVisible();
  await placeTranscriptAtTail(page);
  await startTailBoundaryTrace(page);

  const resolved = page.waitForResponse(
    (response) =>
      response.url().includes(`/checkpoints/${checkpointId}`) &&
      response.request().method() === "POST",
    { timeout: 30_000 },
  );
  await card.getByTestId("approval-approve-primary").click();
  const stream = stage
    .locator(".den-chat-stream")
    .first();
  const streamBox = await stream.boundingBox();
  if (!streamBox) throw new Error("chat viewport is missing");
  await page.mouse.move(
    streamBox.x + streamBox.width / 2,
    streamBox.y + streamBox.height / 2,
  );
  await page.mouse.wheel(0, 2_400);
  expect((await resolved).ok()).toBeTruthy();

  await expect(card).toHaveCount(0, { timeout: 30_000 });
  await expect(stage.getByTestId("composer-chrome-slot-checkpoint")).toHaveCount(0);
  await expectTranscriptTailBounded(page);
  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  expect(Math.max(...trace.overflow)).toBeLessThanOrEqual(2);

  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      {
        id: "00000000-0000-4000-c000-000000000021",
        role: "assistant",
        content: `Approval follow-up: ${"new tail content ".repeat(40)}`,
        created_at: "2026-08-23T18:21:00Z",
        seq: 21,
        ord: 21,
      },
    ]),
  );
  await expect(stage.locator(".transcript-viewport-row").getByText("Approval follow-up:", { exact: false })).toBeVisible({
    timeout: 30_000,
  });
  await expectTranscriptTailBounded(page);
});

webE2e("delayed transcript content cannot publish scroll space past its end", async ({
  page,
  request,
}) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 18 }, (_, index) => ({
        id: `00000000-0000-4000-d000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Delayed content runway ${index + 1}: ${"content ".repeat(18)}`,
        created_at: `2026-08-23T19:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );
  await placeTranscriptAtTail(page);
  await startTailBoundaryTrace(page);

  const stage = liveChatStage(page);
  const lastRow = stage.locator(".transcript-viewport-row[data-msg-id]").last();
  await lastRow.evaluate((row) => {
    const delayed = document.createElement("div");
    delayed.dataset.testDelayedGeometry = "";
    delayed.style.height = "720px";
    row.append(delayed);
  });
  await page.waitForTimeout(100);
  const stream = stage
    .locator(".den-chat-stream")
    .first();
  const box = await stream.boundingBox();
  if (!box) throw new Error("chat viewport is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, 2_400);
  await lastRow.evaluate((row) => {
    row.querySelector("[data-test-delayed-geometry]")?.remove();
  });
  await page.setViewportSize({ width: 760, height: 680 });
  await expectTranscriptTailBounded(page);

  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  expect(Math.max(...trace.overflow)).toBeLessThanOrEqual(2);
});

webE2e("split sidebar restoration stays inside the transcript tail", async ({
  page,
  request,
}) => {
  await page.setViewportSize({ width: 1_500, height: 720 });
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages(
      Array.from({ length: 26 }, (_, index) => ({
        id: `00000000-0000-4000-e000-${String(index + 1).padStart(12, "0")}`,
        role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
        content: `Split resize runway ${index + 1}: ${"width-sensitive transcript content ".repeat(14)}`,
        created_at: `2026-08-23T20:${String(index).padStart(2, "0")}:00Z`,
        seq: index + 1,
        ord: index + 1,
      })),
    ),
  );

  await page
    .getByTestId("focused-project-nav")
    .getByRole("button", { name: "Files", exact: true })
    .click();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await expect(page.getByTestId("split-divider")).toBeVisible();

  await expect(liveChatStage(page).locator('.transcript-viewport-row[data-msg-id]').first()).toBeVisible();
  await expect.poll(() => liveChatStage(page).locator('.den-chat-stream').evaluate((host) => host.scrollHeight > host.clientHeight)).toBe(true);
  await placeTranscriptAtTail(page);
  const row = liveChatStage(page).locator(".transcript-viewport-row[data-msg-id]").last();
  const beforeDrag = await row.boundingBox();
  const divider = page.getByTestId("split-divider");
  const edge = await divider.boundingBox();
  expect(beforeDrag).not.toBeNull();
  expect(edge).not.toBeNull();
  await page.mouse.move(edge!.x + edge!.width / 2, edge!.y + 60);
  await page.mouse.down();
  try {
    await page.mouse.move(edge!.x + 120, edge!.y + 60, { steps: 8 });
    // Dragging changes both row width and wrapped text height.
    await expect.poll(async () => Math.abs((await row.boundingBox())!.width - beforeDrag!.width)).toBeGreaterThan(50);
    await expect.poll(async () => Math.abs((await row.boundingBox())!.height - beforeDrag!.height)).toBeGreaterThan(5);
  } finally {
    await page.mouse.up();
  }

  await page.setViewportSize({ width: 1_300, height: 720 });
  const restore = page.getByTestId("nav-auto-restore-btn").first();
  await expect(restore).toBeVisible();
  await placeTranscriptAtTail(page);
  await startTailBoundaryTrace(page);

  await setTailBoundaryTracePhase(page, "first restore click");
  await restore.click();
  await setTailBoundaryTracePhase(page, "first widen");
  await page.setViewportSize({ width: 1_700, height: 720 });
  await expect(page.getByTestId("nav-collapse-btn")).toBeVisible();
  await setTailBoundaryTracePhase(page, "second contraction");
  await page.setViewportSize({ width: 1_280, height: 720 });
  await expect(restore).toBeVisible();
  await setTailBoundaryTracePhase(page, "second restore click");
  await restore.click();
  await setTailBoundaryTracePhase(page, "second widen");
  await page.setViewportSize({ width: 1_700, height: 720 });

  await expectTranscriptTailBounded(page);
  const trace = await stopTailBoundaryTrace(page);
  expect(trace.overflow.length).toBeGreaterThan(2);
  const worst = trace.samples.reduce((left, right) =>
    right.overflow > left.overflow ? right : left,
  );
  expect(worst.overflow, JSON.stringify(worst)).toBeLessThanOrEqual(2);
});
