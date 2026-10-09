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

import type { Message, WorkerTask } from "../src/api/types.ts";
import { liveChatStage } from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

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

const excerpt = "# E2E minimal Go project";

webE2e("Keep going preserves completed workflow reviewers through accepted verdict", async ({ page, request }, testInfo) => {
  webE2e.setTimeout(180_000);
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  let runId: string | undefined;
  let failed = false;
  const workers = async () => (await apiJson<{ workers: WorkerTask[] }>(
    request, "GET", `/v1/workers?session_id=${sessionId}`,
  )).workers;
  const messages = async () => (await apiJson<{ messages: Message[] }>(
    request, "GET", `/v1/sessions/${sessionId}/messages`,
  )).messages;
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  try {
    const composer = await waitForChatComposerReady(page);
    await composer.fill("/options Compare two ways to organize the fixture README without changing files.");
    await composer.press("Enter");
    await expect.poll(async () => (await apiGetActiveWorkflowRun(request, sessionId)).workflow_id).toBe("options");
    runId = (await apiGetActiveWorkflowRun(request, sessionId)).id;

    // Finish the shipped research topology using real worker tools.
    const read = new Set<string>();
    const finished = new Set<string>();
    for (let step = 0; step < 6; step++) {
      const pending = await apiJson<Pending>(request, "GET", "/harness/llm/pending?wait=30000");
      expect(pending.pending).toBe(true);
      const child = await apiJson<{ parent_session_id?: string }>(request, "GET", `/v1/sessions/${pending.session_id}`);
      expect(child.parent_session_id).toBe(sessionId);
      expect(finished.has(pending.session_id)).toBe(false);
      const completing = read.has(pending.session_id);
      const name = completing ? "complete_leg" : "read";
      expect(pending.tools).toContain(name);
      await apiJson(request, "POST", "/harness/llm/respond", {
        id: pending.id, tool_calls: [{ id: crypto.randomUUID(), name, args: completing ? {
          leg_status: "complete", brief: "Inspected the README.",
          verification: { method: "inspection", reason: "Read the fixture README." },
        } : { path: "README.md" } }],
      });
      if (completing) finished.add(pending.session_id);
      else read.add(pending.session_id);
    }
    expect(finished.size).toBe(3);
    await expect.poll(async () => (await workers()).filter((worker) =>
      finished.has(worker.child_session_id ?? "") && worker.status === "complete",
    ).length).toBe(3);
    await expect.poll(async () => (await apiGetActiveWorkflowRun(request, sessionId)).current_phase).toBe("judge");

    const coordinator = manualHarnessTools(request, sessionId);
    await coordinator.completed(await coordinator.invoke("task", {
      agent_type: "skeptic", scope: { mode: "read" },
      brief: { goal: "Challenge the README organization decision.", done_when: ["Read README.md and report the evidence."] },
    }));
    let reviewer: WorkerTask | undefined;
    await expect.poll(async () => {
      reviewer = (await workers()).find((worker) => worker.agent_type === "skeptic" && worker.workflow_run_id === runId);
      return reviewer?.child_session_id;
    }).toBeTruthy();
    const skeptic = manualHarnessTools(request, reviewer!.child_session_id!);
    await skeptic.completed(await skeptic.invoke("read", { path: "README.md" }));
    await skeptic.completed(await skeptic.invoke("complete_leg", {
      leg_status: "complete", brief: "The README describes a small standalone service fixture.",
      findings: [{ path: "README.md", line: 1, excerpt }],
      verification: { method: "inspection", reason: "Read the fixture README." },
    }));
    await expect.poll(async () => (await workers()).find((worker) => worker.id === reviewer!.id)?.status).toBe("complete");
    const before = await workers();
    const ids = before.map((worker) => worker.id).sort();
    expect(before.filter((worker) => worker.agent_type === "skeptic")).toHaveLength(1);

    // Fail the real coordinator provider stream after its reviewer has settled.
    const pending = await apiJson<Pending>(request, "GET", `/harness/llm/pending?wait=30000&session_id=${sessionId}`);
    expect(pending.pending).toBe(true);
    await apiJson(request, "POST", "/harness/llm/respond", {
      id: pending.id, stream_chunks: [{ error: "Fixture provider disconnected", done: true }],
    });
    await expect.poll(async () => (await apiJson<{ status: string }>(request, "GET", `/v1/sessions/${sessionId}`)).status).toBe("idle");
    await expect(liveChatStage(page).getByTestId("turn-error-row")).toBeVisible();
    await expect(liveChatStage(page).getByTestId("turn-error-keep-going")).toBeEnabled();
    const anchor = (await messages()).at(-1)!.id;
    const recoveryRequest = page.waitForRequest((req) => req.method() === "POST" && req.url().endsWith(`/v1/sessions/${sessionId}/prompts`));
    await liveChatStage(page).getByTestId("turn-error-keep-going").click();
    expect((await recoveryRequest).postDataJSON().recovery).toEqual({ action: "continue", after_message_id: anchor });
    await expect.poll(async () => (await messages()).filter((message) => message.kind === "user_continuation").length).toBe(1);

    // The parent has never read this file: verdict grounding must retain reviewer evidence.
    await coordinator.completed(await coordinator.invoke("submit_verdict", {
      verdict: { verdict: "SELECTED", winner: "Keep the current organization", rationale: "The README already describes the service boundary." },
      cited_evidence: [{ path: "README.md", line: 1, excerpt }],
    }));
    expect((await apiGetActiveWorkflowRun(request, sessionId)).id).toBe(runId);
    const after = await workers();
    expect(after.map((worker) => worker.id).sort()).toEqual(ids);
    expect(after.find((worker) => worker.id === reviewer!.id)?.status).toBe("complete");
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    try {
      if (runId) {
        const run = await apiGetActiveWorkflowRun(request, sessionId);
        await apiJson(request, "POST", `/v1/workflow-runs/${runId}/cancel`, { expected_revision: run.revision, reason: "Fixture completed" });
      }
    } catch (error) {
      if (!failed) throw error;
      await testInfo.attach("workflow-cleanup-error", { body: String(error), contentType: "text/plain" });
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Harness reply." });
    }
  }
});
