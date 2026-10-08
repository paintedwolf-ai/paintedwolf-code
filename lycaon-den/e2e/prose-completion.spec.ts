import { expect } from "@playwright/test";
import type { SettingsLimitsResponse } from "../src/api/types.ts";
import { apiJson, bootstrapChatSession, liveChatStage, settledChatSessionId, waitForChatComposerReady, webE2e } from "./helpers.ts";

webE2e("a tool-only forced final response shows an error and permits recovery", async ({ page, request }, testInfo) => {
  const project = await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const limits = await apiJson<SettingsLimitsResponse>(request, "GET", "/v1/settings/limits");
  let effectiveLimits: SettingsLimitsResponse | undefined;
  try {
    await apiJson(request, "PATCH", "/v1/settings/limits", { max_iterations: 1 });
    effectiveLimits = await apiJson<SettingsLimitsResponse>(request, "GET", `/v1/settings/limits?project_id=${project.id}`);
    expect(effectiveLimits.max_iterations).toBe(1);
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
    const composer = await waitForChatComposerReady(page);
    await composer.fill("Read the fixture README.");
    await composer.press("Enter");
    const pending = await apiJson<{ pending: boolean; id: string; session_id: string; tools: string[] }>(request, "GET", `/harness/llm/pending?wait=30000&session_id=${sessionId}`);
    expect(pending.pending).toBe(true);
    expect(pending.session_id).toBe(sessionId);
    expect(pending.tools).toContain("read");
    await apiJson(request, "POST", "/harness/llm/respond", {
      id: pending.id, content: "", tool_calls: [{ id: crypto.randomUUID(), name: "read", args: { path: "README.md" } }],
    });
    await expect(page.getByRole("status", { name: "Status messages", exact: true })).toContainText("Model skipped its closing summary", { timeout: 30_000 });
    // The failed closeout ends the turn without a blank answer or another model request.
    await expect.poll(async () => (await apiJson<{ status: string }>(request, "GET", `/v1/sessions/${sessionId}`)).status).toBe("idle");
    const followup = await apiJson<{ pending: boolean }>(request, "GET", `/harness/llm/pending?session_id=${sessionId}`);
    expect(followup.pending).toBe(false);
    await expect(liveChatStage(page).getByTestId("transcript-article-assistant")).toHaveCount(0);
    await apiJson(request, "PATCH", "/v1/settings/limits", { max_iterations: limits.max_iterations });
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "The conversation can continue." });
    await composer.fill("Continue with a brief acknowledgement.");
    await composer.press("Enter");
    await expect(liveChatStage(page).getByTestId("transcript-article-assistant").last()).toContainText("The conversation can continue.", { timeout: 30_000 });
  } catch (error) {
    const diagnostic = {
      effectiveLimits,
      session: await apiJson(request, "GET", `/v1/sessions/${sessionId}`),
      transcript: await apiJson(request, "GET", `/v1/sessions/${sessionId}/messages`),
      pending: await apiJson(request, "GET", "/harness/llm/pending"),
    };
    await testInfo.attach("closeout-state", { body: JSON.stringify(diagnostic), contentType: "application/json" });
    throw error;
  } finally {
    await apiJson(request, "PATCH", "/v1/settings/limits", { max_iterations: limits.max_iterations });
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Harness reply." });
  }
});
