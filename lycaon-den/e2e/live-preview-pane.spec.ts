import {
  expect,
  test,
  type APIRequestContext,
} from "@playwright/test";
import {
  activateProject,
  apiSeedSessionTranscript,
  apiConfig,
  apiFindHarnessProject,
  liveChatStage,
  openNewChatSession,
  transcriptMessages,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

async function emitPreview(
  request: APIRequestContext,
  event: Record<string, unknown>,
): Promise<void> {
  const { apiUrl, token } = apiConfig();
  const response = await request.post(`${apiUrl}/harness/preview`, {
    headers: { Authorization: `Bearer ${token}` },
    data: event,
  });
  expect(response.ok(), await response.text()).toBeTruthy();
}

webE2e("den:harness — live preview pane attach / drive / end", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);
  await page.goto("/");
  await waitHarnessConnected(page);
  await page.setViewportSize({ width: 1280, height: 550 });

  const project = await apiFindHarnessProject(request);
  const { apiUrl, token } = apiConfig();
  const auth = { Authorization: `Bearer ${token}` };

  await activateProject(page, project.id);
  const sessionId = await openNewChatSession(page);
  const assistantMessageId = crypto.randomUUID();
  const toolCallId = `tool-preview-${crypto.randomUUID()}`;
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      {
        id: assistantMessageId,
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: toolCallId,
            name: "capture_page",
            args: { url: "http://127.0.0.1:9/" },
          },
        ],
        created_at: "2026-08-16T12:00:00Z",
        seq: 1,
        ord: 1,
      },
      {
        id: crypto.randomUUID(),
        role: "tool",
        content: "captured",
        tool_result: {
          tool_call_id: toolCallId,
          assistant_message_id: assistantMessageId,
          tool: "capture_page",
          content: "captured",
        },
        created_at: "2026-08-16T12:00:01Z",
        seq: 2,
        ord: 1,
      },
    ]),
  );
  await expect(liveChatStage(page).locator('[data-tool="capture_page"]')).toBeAttached({
    timeout: 30_000,
  });

  const activity = liveChatStage(page).getByTestId("activity-span-card");
  if (!await activity.evaluate((element) => element.hasAttribute("open"))) {
    await activity.locator(":scope > summary").click();
  }
  const tool = liveChatStage(page).locator('[data-tool="capture_page"]');
  if (!await tool.evaluate((element) => element.hasAttribute("open"))) {
    await tool.locator(":scope > summary").click();
  }

  for (const event of [
    {
      op: "attach",
      session_id: sessionId,
      page_id: "page-test",
      assistant_message_id: assistantMessageId,
      tool_call_id: toolCallId,
      seq: 1,
      url: "http://127.0.0.1:9/",
      title: "Harness page",
    },
    {
      op: "frame",
      session_id: sessionId,
      page_id: "page-test",
      assistant_message_id: assistantMessageId,
      tool_call_id: toolCallId,
      seq: 2,
      mime: "image/jpeg",
      jpeg_b64:
        "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAn/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIQAxAAAAGcP//EABQQAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQEAAQUCf//EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQMBAT8Bf//EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAIAQIBAT8Bf//Z",
    },
    {
      op: "action",
      session_id: sessionId,
      page_id: "page-test",
      assistant_message_id: assistantMessageId,
      tool_call_id: toolCallId,
      seq: 3,
      action: { label: "click · #go", target: "#go", x: 10, y: 10, w: 40, h: 16 },
    },
  ]) {
    await emitPreview(request, event);
  }

  await expect(page.getByTestId("live-preview-pane")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("live-preview-action")).toContainText("click");
  await expect(page.getByTestId("live-preview-status")).toHaveAttribute(
    "data-status",
    /live|busy/,
  );

  // Driving state is busy while the driver controls the page, then returns to live.
  await emitPreview(request, {
      op: "state",
      session_id: sessionId,
      page_id: "page-test",
      assistant_message_id: assistantMessageId,
      tool_call_id: toolCallId,
      seq: 4,
      idle: false,
  });
  await expect(page.getByTestId("live-preview-status")).toHaveAttribute(
    "data-status",
    "busy",
  );
  await emitPreview(request, {
      op: "state",
      session_id: sessionId,
      page_id: "page-test",
      assistant_message_id: assistantMessageId,
      tool_call_id: toolCallId,
      seq: 5,
      idle: true,
  });
  await expect(page.getByTestId("live-preview-status")).toHaveAttribute(
    "data-status",
    "live",
  );

  await emitPreview(request, {
      op: "detach",
      session_id: sessionId,
      page_id: "page-test",
      assistant_message_id: assistantMessageId,
      tool_call_id: toolCallId,
      seq: 6,
  });

  // The source session remains in place as ended. Its final visual must not
  // collapse to the blank idle pane while the recording is attached below it.
  await expect(page.getByTestId("live-preview-status")).toHaveAttribute(
    "data-status",
    "ended",
    { timeout: 15_000 },
  );
  await expect(page.getByTestId("live-preview-stage").locator("img")).toBeVisible({
    timeout: 15_000,
  });

  const watch = await request.post(
    `${apiUrl}/v1/sessions/${sessionId}/previews/watch`,
    {
      headers: { ...auth, "Content-Type": "application/json" },
      data: { watching: false, page_id: "page-test" },
    },
  );
  expect(watch.ok(), await watch.text()).toBeTruthy();
  // Unauthenticated watch must fail (security: same bearer channel).
  const denied = await request.post(
    `${apiUrl}/v1/sessions/${sessionId}/previews/watch`,
    {
      headers: { "Content-Type": "application/json" },
      data: { watching: true, page_id: "page-test" },
    },
  );
  expect(denied.ok()).toBeFalsy();
});
