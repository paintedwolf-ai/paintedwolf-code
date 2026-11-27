import { expect } from "@playwright/test";
import { manualHarnessTools } from "./manual-harness-tools.ts";
import {
  activateProject,
  apiConfig,
  apiJson,
  apiPostPrompt,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  webE2e,
} from "./helpers.ts";

interface BackgroundProcessItem {
  process_id: string;
  running: boolean;
  termination_reason?: string;
  exit_code?: number;
}

webE2e.describe("portable command stop live qualification", () => {
  webE2e(
    "rendered Project Running Stop ends continued program and held job while sibling survives for live command_stop",
    async ({ page, request }) => {
      webE2e.setTimeout(120_000);
      const project = await bootstrapChatSession(page, request);
      const stream = liveChatStage(page).getByTestId("chat-stream");
      await expect
        .poll(async () => (await stream.getAttribute("data-session-id")) ?? "", { timeout: 30_000 })
        .not.toBe("");
      const sessionId = (await stream.getAttribute("data-session-id"))!;
      const { apiUrl, token } = apiConfig();
      const headers = { Authorization: `Bearer ${token}` };

      const listSessionBg = async (): Promise<BackgroundProcessItem[]> => {
        const res = await request.get(`${apiUrl}/v1/sessions/${sessionId}/background`, { headers });
        expect(res.ok(), await res.text()).toBeTruthy();
        const body = (await res.json()) as { processes: BackgroundProcessItem[] };
        return body.processes ?? [];
      };

      const parseToolJSON = <T,>(raw: string): T => {
        const start = raw.indexOf("{");
        expect(start, `expected JSON object in tool result: ${raw}`).toBeGreaterThanOrEqual(0);
        return JSON.parse(raw.slice(start)) as T;
      };

      await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
      try {
        const prompt = await apiPostPrompt(request, sessionId, {
          text: "Start a sibling background job and a continued command program.",
        });
        expect(prompt.ok(), await prompt.text()).toBe(true);
        const driver = manualHarnessTools(request, sessionId);

        // 1. Start an independent sibling background job.
        const siblingCallId = await driver.invoke("command", {
          command: "sleep 300 & echo sibling-started",
        });
        await driver.completed(siblingCallId);
        const afterSibling = await listSessionBg();
        const siblingProc = afterSibling.find((p) => p.running);
        expect(siblingProc, JSON.stringify(afterSibling)).toBeTruthy();
        const siblingHandle = siblingProc!.process_id;

        // 2. Start a program that holds a & job and runs a foreground external process
        // past the 30s inline budget so it promotes to a continued program handle.
        const continuedCallId = await driver.invoke("command", {
          command: "sleep 300 & external -- /bin/sh -c 'sleep 300'",
        });
        const continuedContent = await driver.completed(continuedCallId);
        const continuedResult = parseToolJSON<{
          program_id: string;
          state: string;
          handle: string;
        }>(continuedContent);
        expect(continuedResult.state).toBe("running");
        expect(continuedResult.handle).toBeTruthy();
        const continuedHandle = continuedResult.handle;

        const liveBeforeStop = await listSessionBg();
        const heldProc = liveBeforeStop.find(
          (p) => p.running && p.process_id !== siblingHandle && p.process_id !== continuedHandle,
        );
        expect(heldProc, JSON.stringify(liveBeforeStop)).toBeTruthy();
        const heldJobHandle = heldProc!.process_id;

        // 3. Verify project background API sees all three handles, then hydrate
        // the rendered Den Project Running view and click Stop on continuedHandle.
        await expect
          .poll(
            async () => {
              const res = await request.get(`${apiUrl}/v1/projects/${project.id}/background`, { headers });
              if (!res.ok()) return false;
              const body = (await res.json()) as { processes: BackgroundProcessItem[] };
              const ids = new Set((body.processes ?? []).filter((p) => p.running).map((p) => p.process_id));
              return ids.has(continuedHandle) && ids.has(heldJobHandle) && ids.has(siblingHandle);
            },
            { timeout: 15_000 },
          )
          .toBe(true);

        await page.reload();
        await expectShellReady(page);
        if (!(await page.getByTestId("focused-project-nav").isVisible().catch(() => false))) {
          await activateProject(page, project.id);
        }
        await page.getByTestId("project-running-entry").click();
        await expect(page.getByTestId("project-running-view")).toBeVisible({ timeout: 15_000 });
        const stopButton = page.getByTestId(`project-running-stop-${continuedHandle}`);
        await expect(stopButton).toBeVisible({ timeout: 15_000 });

        const stopResponsePromise = page.waitForResponse(
          (resp) =>
            resp.request().method() === "POST" &&
            resp.url().includes(`/v1/projects/${project.id}/background/${continuedHandle}/stop`),
        );
        await stopButton.click();
        const stopResponse = await stopResponsePromise;
        expect(stopResponse.status()).toBe(200);
        const immediateAck = (await stopResponse.json()) as {
          process_id: string;
          stop_requested: boolean;
          running: boolean;
          termination_reason?: string;
        };
        expect(immediateAck.process_id).toBe(continuedHandle);
        expect(immediateAck.stop_requested).toBe(true);
        expect(immediateAck.termination_reason).toBe("stopped_by_user");
        console.log(
          "DEVAPP_QUAL",
          JSON.stringify({
            control: `project-running-stop-${continuedHandle}`,
            route: `POST /v1/projects/${project.id}/background/${continuedHandle}/stop`,
            target_handle: continuedHandle,
            held_job_handle: heldJobHandle,
            sibling_handle: siblingHandle,
            immediate_ack: immediateAck,
          }),
        );

        // Wait for both continuedHandle and its held & job to settle as stopped_by_user,
        // while siblingHandle remains running.
        let settledSnapshot: BackgroundProcessItem[] = [];
        await expect
          .poll(
            async () => {
              settledSnapshot = await listSessionBg();
              const cont = settledSnapshot.find((p) => p.process_id === continuedHandle);
              const held = settledSnapshot.find((p) => p.process_id === heldJobHandle);
              const sib = settledSnapshot.find((p) => p.process_id === siblingHandle);
              return (
                cont?.running === false &&
                cont?.termination_reason === "stopped_by_user" &&
                held?.running === false &&
                held?.termination_reason === "stopped_by_user" &&
                sib?.running === true
              );
            },
            { timeout: 15_000 },
          )
          .toBe(true);

        console.log(
          "DEVAPP_SETTLED",
          JSON.stringify({
            continued: settledSnapshot.find((p) => p.process_id === continuedHandle),
            held_job: settledSnapshot.find((p) => p.process_id === heldJobHandle),
            sibling: settledSnapshot.find((p) => p.process_id === siblingHandle),
          }),
        );

        // 4. Exercise the live model command_stop tool on the surviving sibling handle.
        await driver.loadTools("command_stop");
        const modelStopCallId = await driver.invoke("command_stop", { handle: siblingHandle });
        const modelStopContent = await driver.completed(modelStopCallId);
        const modelStopResult = parseToolJSON<{
          handle: string;
          stop_requested: boolean;
          running: boolean;
          termination_reason?: string;
        }>(modelStopContent);
        expect(modelStopResult.handle).toBe(siblingHandle);
        expect(modelStopResult.stop_requested).toBe(true);
        expect(modelStopResult.termination_reason).toBe("stopped_by_agent");

        let finalSnapshot: BackgroundProcessItem[] = [];
        await expect
          .poll(
            async () => {
              finalSnapshot = await listSessionBg();
              const sib = finalSnapshot.find((p) => p.process_id === siblingHandle);
              return sib?.running === false && sib?.termination_reason === "stopped_by_agent";
            },
            { timeout: 15_000 },
          )
          .toBe(true);

        console.log(
          "LIVE_MODEL_COMMAND_STOP",
          JSON.stringify({
            tool: "command_stop",
            target_handle: siblingHandle,
            tool_result: modelStopResult,
            settled: finalSnapshot.find((p) => p.process_id === siblingHandle),
          }),
        );

        // Complete the turn cleanly.
        const pendingRes = await request.get(
          `${apiUrl}/harness/llm/pending?wait=30000&session_id=${sessionId}`,
          { headers },
        );
        if (pendingRes.ok()) {
          const pending = (await pendingRes.json()) as { pending: boolean; id: string };
          if (pending.pending) {
            await request.post(`${apiUrl}/harness/llm/respond`, {
              headers,
              data: { id: pending.id, content: "Stopped continued program and sibling job.", tool_calls: [] },
            });
          }
        }
      } finally {
        await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Done." });
      }
    },
  );
});
