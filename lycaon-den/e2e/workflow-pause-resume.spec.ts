import { expect } from "@playwright/test";
import {
  apiGetActiveWorkflowRun,
  apiConfig,
  apiJson,
  bootstrapChatSession,
  openWorkflowsTab,
  settledChatSessionId,
  waitForChatComposerReady,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

type Pending = { pending: boolean; id: string; session_id: string; tools: string[] };

webE2e("resume reconciles workers that finished while paused", async ({ page, request }) => {
  webE2e.setTimeout(180_000);
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  let runId: string | undefined;
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  try {
    const composer = await waitForChatComposerReady(page);
    await composer.fill("/options Compare two ways to organize the fixture README. Inspect it without editing files.");
    await composer.press("Enter");
    await expect.poll(async () => (await apiGetActiveWorkflowRun(request, sessionId)).workflow_id).toBe("options");
    runId = (await apiGetActiveWorkflowRun(request, sessionId)).id;
    const nextWorker = async (): Promise<Pending> => {
      for (let attempt = 0; attempt < 20; attempt++) {
        const pending = await apiJson<Pending>(request, "GET", "/harness/llm/pending?wait=30000");
        expect(pending.pending).toBe(true);
        const { apiUrl, token } = apiConfig();
        const response = await request.get(`${apiUrl}/v1/sessions/${pending.session_id}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (response.ok() && (await response.json()).parent_session_id === sessionId) return pending;
        expect(pending.session_id, "coordinator must remain held during fan out").not.toBe(sessionId);
        // Auto mode only affects future calls; earlier fixture requests can still be pending.
        await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Fixture complete." });
      }
      throw new Error("No worker request for this workflow");
    };
    const first = await nextWorker();
    expect(first.pending).toBe(true);
    expect(first.session_id).not.toBe(sessionId);
    await openWorkflowsTab(page);
    await page.getByRole("button", { name: "Pause", exact: true }).click();
    await expect.poll(async () => (await apiGetActiveWorkflowRun(request, sessionId)).status).toBe("paused");
    const paused = await apiGetActiveWorkflowRun(request, sessionId);
    const read = new Set<string>();
    const finished = new Set<string>();
    for (let step = 0; step < 6; step++) {
      const pending = step === 0 ? first : await nextWorker();
      expect(pending.pending).toBe(true);
      expect(pending.session_id).not.toBe(sessionId);
      expect(finished.has(pending.session_id)).toBe(false);
      const completing = read.has(pending.session_id);
      const name = completing ? "complete_leg" : "read";
      expect(pending.tools).toContain(name);
      await apiJson(request, "POST", "/harness/llm/respond", {
        id: pending.id, content: "", tool_calls: [{
          id: crypto.randomUUID(), name,
          args: completing ? {
            leg_status: "complete", brief: "Inspected the README without changes.",
            verification: { method: "inspection", reason: "Read the fixture README." },
          } : { path: "README.md" },
        }],
      });
      if (completing) finished.add(pending.session_id);
      else read.add(pending.session_id);
    }
    expect(finished.size).toBe(3);
    // The topology result commits a run revision after the workers settle.
    await expect.poll(async () => (await apiGetActiveWorkflowRun(request, sessionId)).revision).toBeGreaterThan(paused.revision);
    expect((await apiGetActiveWorkflowRun(request, sessionId)).current_phase).toBe("fan_out");
    await page.reload();
    await waitHarnessConnected(page);
    await openWorkflowsTab(page);
    await page.getByRole("button", { name: "Resume", exact: true }).click();
    await expect.poll(async () => (await apiGetActiveWorkflowRun(request, sessionId)).current_phase).toBe("judge");
    const resumed = await apiJson<Pending>(request, "GET", "/harness/llm/pending?wait=30000");
    expect(resumed.pending).toBe(true);
    expect(resumed.session_id).toBe(sessionId);
  } finally {
    if (runId) {
      const run = await apiGetActiveWorkflowRun(request, sessionId);
      await apiJson(request, "POST", `/v1/workflow-runs/${runId}/cancel`, {
        expected_revision: run.revision, reason: "Fixture completed",
      });
    }
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Harness reply." });
  }
});
