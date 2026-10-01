import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect } from "@playwright/test";
import {
  bootstrapChatSession,
  apiConfig,
  apiJson,
  liveChatStage,
  settledChatSessionId,
  waitForChatComposerReady,
  waitForSessionReady,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import type { Message } from "../src/api/types.ts";

webE2e("interrupted tool settles after restart and preserves the unsent draft", async ({ page, context, request }) => {
  webE2e.setTimeout(360_000);
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  const composer = await waitForChatComposerReady(page);
  await composer.fill("Run one bounded command for interruption recovery.");
  await composer.press("Enter");
  const pendingPath = "/harness/llm/pending?wait=30000";
  type Pending = { pending: boolean; id: string; session_id: string };
  let pending = await apiJson<Pending>(request, "GET", pendingPath);
  expect(pending.session_id).toBe(sessionId);
  await apiJson(request, "POST", "/harness/llm/respond", {
    id: pending.id, tool_calls: [{ id: "load-command", name: "request_tools", args: { requests: ["command"] } }],
  });
  pending = await apiJson<Pending>(request, "GET", pendingPath);
  expect(pending.session_id).toBe(sessionId);
  await apiJson(request, "POST", "/harness/llm/respond", {
    id: pending.id, tool_calls: [{ id: "bounded-command", name: "command", args: {
      command: "node -e \"console.log('recovery started'); setTimeout(() => console.log('recovery finished'), 90000)\"",
      timeout_ms: 120000,
    } }],
  });
  await expect(liveChatStage(page)).toContainText("Running command ·", { timeout: 30_000 });
  const draft = "Unsent recovery draft — café 🌱";
  await composer.fill(draft);
  await page.evaluate(async () => {
    const state = await import(/* @vite-ignore */ "/src/chat/composer/" + "composer-document-store.ts");
    await state.flushComposerDocumentsToDisk();
  });
  await page.close();
  const restart = fileURLToPath(new URL("../../scripts/harness/crash-restart.sh", import.meta.url));
  await promisify(execFile)("bash", [restart], { timeout: 300_000 });
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "The interrupted command was not retried." });
  await waitForSessionReady(request, sessionId);
  const reopened = await context.newPage();
  try {
    await reopened.goto("/");
    await waitHarnessConnected(reopened);
    await expect.poll(() => settledChatSessionId(reopened)).toBe(sessionId);
    const restored = await waitForChatComposerReady(reopened);
    await expect(restored).toHaveValue(draft);
    await expect(reopened.getByRole("button", { name: "Send message", exact: true })).toBeEnabled();
    await expect(liveChatStage(reopened)).not.toContainText("Running command ·");
    const transcript = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`);
    const calls = transcript.messages.flatMap((message) => (message.tool_calls ?? []).filter((call) => call.name === "command").map((call) => ({ message, call })));
    expect(calls).toHaveLength(1);
    const command = calls[0];
    if (!command) throw new Error("Expected the single interrupted command");
    const result = transcript.messages.find((message) => message.tool_result?.tool_call_id === command.call.id)?.tool_result;
    expect(result?.assistant_message_id).toBe(command.message.id);
    expect(result?.invocation?.status).toBe("interrupted");
  } finally {
    await reopened.close();
  }
});

webE2e("cold window restores its conversation after the sidecar revision changes", async ({ page, context }) => {
  webE2e.setTimeout(360_000);
  await bootstrapChatSession(page);
  const { apiUrl, token } = apiConfig();
  const automatic = await page.request.post(`${apiUrl}/harness/llm/auto`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { enabled: true, text: "Restart fixture remembered." },
  });
  expect(automatic.ok(), await automatic.text()).toBeTruthy();
  const sessionId = await settledChatSessionId(page);
  const prompt = "Remember this restart recovery fixture.";
  const composer = await waitForChatComposerReady(page);
  await composer.fill(prompt);
  await composer.press("Enter");
  await expect(liveChatStage(page).getByTestId("transcript-article-user").filter({ hasText: prompt })).toBeVisible();
  await expect(liveChatStage(page).getByTestId("transcript-article-assistant")).toBeVisible();
  await waitForSessionReady(page.request, sessionId);
  await page.evaluate(async () => {
    const state = await import(/* @vite-ignore */ "/src/store/" + "app-state-snapshot.ts");
    await state.flushAppState();
  });
  await page.close();
  const restart = fileURLToPath(new URL("../../scripts/harness/crash-restart.sh", import.meta.url));
  await promisify(execFile)("bash", [restart], { timeout: 300_000 });

  const reopened = await context.newPage();
  try {
    await reopened.goto("/");
    await waitHarnessConnected(reopened);
    await expect.poll(() => settledChatSessionId(reopened)).toBe(sessionId);
    await expect(liveChatStage(reopened).getByTestId("transcript-article-user").filter({ hasText: prompt })).toHaveCount(1);
    await expect(liveChatStage(reopened).getByTestId("transcript-article-assistant")).toBeVisible();
  } finally {
    await reopened.close();
  }
});
