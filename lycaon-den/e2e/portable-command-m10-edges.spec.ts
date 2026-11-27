import { expect, type Locator, type Page, type Response } from "@playwright/test";
import type { CommandProgramReceiptPage, CommandReceiptDocumentPage, Message } from "../src/api/types.ts";
import {
  activateProject,
  apiJson,
  apiPostPrompt,
  apiSeedSessionTranscript,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  openSessionRow,
  settledChatSessionId,
  transcriptMessages,
  waitForSessionReady,
  webE2e,
} from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

type PendingTurn = { pending: boolean; id: string; session_id: string };

type DocHit = {
  status: number;
  revision: string | null;
  settled: boolean | null;
  totalRows: number;
  rowsLength: number;
  complete: boolean | null;
};

type ReceiptHit = {
  status: number;
  state: string | null;
  activityRevision: number | null;
  executionsLength: number;
};

type WindowHit = {
  status: number;
  windowId: string;
  errorCode: string | null;
};

function attachEdgeRecorder(page: Page) {
  const docs: DocHit[] = [];
  const receipts: ReceiptHit[] = [];
  const windows: WindowHit[] = [];
  const inFlight = new Set<Promise<void>>();

  const record = async (response: Response) => {
    const parsed = new URL(response.url());
    const status = response.status();

    if (parsed.pathname.endsWith("/program-receipt-document")) {
      let body: Partial<CommandReceiptDocumentPage> | undefined;
      try { body = (await response.json()) as Partial<CommandReceiptDocumentPage>; } catch { /* non-JSON */ }
      docs.push({
        status,
        revision: body?.revision ?? null,
        settled: body?.settled ?? null,
        totalRows: body?.total_rows ?? 0,
        rowsLength: body?.rows?.length ?? 0,
        complete: body?.complete ?? null,
      });
      return;
    }

    if (parsed.pathname.includes("/program-receipts/")) {
      let body: Partial<CommandProgramReceiptPage> | undefined;
      try { body = (await response.json()) as Partial<CommandProgramReceiptPage>; } catch { /* non-JSON */ }
      receipts.push({
        status,
        state: body?.program?.state ?? null,
        activityRevision: body?.program?.activity_revision ?? null,
        executionsLength: body?.executions?.length ?? 0,
      });
      return;
    }

    if (parsed.pathname.includes("/source/command-windows/")) {
      const windowId = parsed.pathname.split("/source/command-windows/")[1]?.split("/")[0] ?? "";
      let body: { code?: string } | undefined;
      try { body = (await response.json()) as { code?: string }; } catch { /* non-JSON */ }
      windows.push({ status, windowId, errorCode: body?.code ?? null });
    }
  };

  page.on("response", (response) => {
    const task = record(response).finally(() => { inFlight.delete(task); });
    inFlight.add(task);
  });

  return {
    docs,
    receipts,
    windows,
    async drain() { await Promise.all([...inFlight]); },
  };
}

async function expandDisclosure(container: Locator) {
  await expect(container).toBeVisible({ timeout: 30_000 });
  if ((await container.getAttribute("data-expanded")) !== "true") {
    await container.locator(":scope > .den-transcript-disclosure-header").click();
  }
  await expect(container).toHaveAttribute("data-expanded", "true");
}

webE2e.describe("portable command M10 live edges", () => {
  webE2e("live-to-settled receipt document refreshes across a changed revision", async ({ page, request }, testInfo) => {
    webE2e.setTimeout(180_000);
    const recorder = attachEdgeRecorder(page);

    await bootstrapChatSession(page, request, { debug: { verboseMode: true } });
    const sessionId = await settledChatSessionId(page);
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });

    try {
      const prompt = await apiPostPrompt(request, sessionId, {
        text: "Run a multi-step portable command that advances across steps.",
      });
      expect(prompt.ok(), await prompt.text()).toBe(true);

      const driver = manualHarnessTools(request, sessionId);
      const commandText = "printf 'step1\\n' && sleep 5 && printf 'step2\\n' && sleep 2 && printf 'step3\\n'";
      const callId = await driver.invoke("command", { command: commandText });

      // Open the live folded command activity and its receipt stage while the command is still running.
      const span = liveChatStage(page).getByTestId("activity-span-card").last();
      await expandDisclosure(span);
      const card = span.getByTestId("action-card").first();
      await expandDisclosure(card);
      const receiptLink = card.getByTestId("action-link-receipt");
      await expect(receiptLink).toBeVisible({ timeout: 15_000 });
      await receiptLink.click();

      const reader = page.getByTestId("chat-content-page");
      await expect(reader).toBeVisible({ timeout: 15_000 });

      await expect.poll(async () => {
        await recorder.drain();
        return recorder.docs.some((entry) => entry.status === 200 && entry.settled === false && entry.revision !== null);
      }, { timeout: 15_000 }).toBe(true);

      const liveDoc = recorder.docs.find((entry) => entry.status === 200 && entry.settled === false && entry.revision !== null)!;
      const refreshBtn = page.getByTestId("chat-process-pretty-refresh");
      await expect(refreshBtn).toBeVisible({ timeout: 10_000 });

      // Wait for the multi-step command to complete and settle.
      const output = await driver.completed(callId);
      expect(output).toContain("step3");

      // Refresh the open receipt snapshot and verify the changed settled revision and final rows render.
      await refreshBtn.click();
      await expect.poll(async () => {
        await recorder.drain();
        return recorder.docs.some((entry) => entry.status === 200 && entry.settled === true && entry.revision !== null);
      }, { timeout: 20_000 }).toBe(true);

      const settledDoc = recorder.docs.findLast((entry) => entry.status === 200 && entry.settled === true && entry.revision !== null)!;
      expect(settledDoc.revision).not.toBe(liveDoc.revision);
      expect(settledDoc.totalRows).toBeGreaterThanOrEqual(liveDoc.totalRows);
      await expect(refreshBtn).toHaveCount(0, { timeout: 10_000 });
      await expect(reader).toContainText('"state":"completed"', { timeout: 15_000 });
      await expect(reader).toContainText('"status_code":0', { timeout: 15_000 });
      await expect(reader).toContainText('"total_lines":3', { timeout: 15_000 });

      const pending = await apiJson<PendingTurn>(request, "GET", `/harness/llm/pending?wait=30000&session_id=${sessionId}`);
      expect(pending.pending).toBe(true);
      await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "All three steps completed." });
      await waitForSessionReady(request, sessionId);

      const receiptRevisions = recorder.receipts
        .filter((entry) => entry.status === 200 && entry.activityRevision !== null)
        .map((entry) => entry.activityRevision as number);
      for (let i = 1; i < receiptRevisions.length; i += 1) {
        expect(receiptRevisions[i]).toBeGreaterThanOrEqual(receiptRevisions[i - 1] ?? 0);
      }

      console.log("M10_EDGES_EVIDENCE:" + JSON.stringify({
        case: "changed_revision",
        sessionId,
        rev1: liveDoc.revision,
        rev2: settledDoc.revision,
        revisionChanged: settledDoc.revision !== liveDoc.revision,
        settled: settledDoc.settled,
        totalRowsBefore: liveDoc.totalRows,
        totalRowsAfter: settledDoc.totalRows,
        receiptRevisions,
      }));
    } catch (error) {
      const transcript = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`).catch(() => undefined);
      await testInfo.attach("m10-edges-changed-rev-failure", {
        body: JSON.stringify({ transcript, docs: recorder.docs, receipts: recorder.receipts }, null, 2),
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

  webE2e("pruned source command window 404 stops re-polling cleanly", async ({ page, request }) => {
    webE2e.setTimeout(120_000);
    const recorder = attachEdgeRecorder(page);
    const prunedWindowId = "m10-pruned-window-1";

    const project = await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const carrier = crypto.randomUUID();
    await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
      { id: crypto.randomUUID(), role: "user", content: "Check pruned source window handling.", created_at: "2026-11-26T20:00:00Z", seq: 1, ord: 1 },
      {
        id: carrier, role: "assistant", content: "", status: "complete", created_at: "2026-11-26T20:00:01Z", seq: 2, ord: 2,
        tool_calls: [{ id: "call_pruned_win", name: "read", args: { path: "README.md" } }],
      },
      {
        id: crypto.randomUUID(), role: "tool", content: "ok", created_at: "2026-11-26T20:00:02Z", seq: 3, ord: 3,
        tool_result: {
          tool_call_id: "call_pruned_win",
          assistant_message_id: carrier,
          tool: "read",
          content: "ok",
          outcome: "completed",
          card_facts: [{ kind: "source_observation", source_windows: [prunedWindowId] }],
        },
      },
    ]));

    await page.reload();
    await expectShellReady(page);
    await activateProject(page, project.id);
    await openSessionRow(page, sessionId);

    const stream = liveChatStage(page).locator(`[data-testid="chat-stream"][data-session-id="${sessionId}"]`);
    const span = stream.getByTestId("activity-span-card").last();
    await expandDisclosure(span);
    const card = stream.locator('[data-testid="action-card"][data-tool-call-id="call_pruned_win"]');
    await expect(card).toBeVisible({ timeout: 30_000 });

    // Real sidecar returns 404 source_history_not_found for the pruned window.
    await expect.poll(async () => {
      await recorder.drain();
      return recorder.windows.filter((entry) => entry.windowId === prunedWindowId).length;
    }, { timeout: 15_000 }).toBeGreaterThanOrEqual(1);

    await page.waitForTimeout(500);
    await recorder.drain();
    const initialHits = recorder.windows.filter((entry) => entry.windowId === prunedWindowId);
    expect(initialHits.length).toBeGreaterThanOrEqual(1);
    expect(initialHits.length).toBeLessThanOrEqual(2);
    for (const hit of initialHits) {
      expect(hit.status).toBe(404);
      expect(hit.errorCode).toBe("source_history_not_found");
    }
    const settledCount = initialHits.length;

    // Dispatch repeated source invalidations and wait a 3-second quiet window; Den must not re-poll.
    await page.evaluate(async (projectId: string) => {
      const mod = await import(/* @vite-ignore */ "/src/files/source/" + "source-invalidation.ts");
      mod.invalidateSourceQueries({ projectId });
      mod.invalidateSourceQueries({ projectId });
    }, project.id);

    await page.waitForTimeout(3_000);
    await recorder.drain();

    const prunedHits = recorder.windows.filter((entry) => entry.windowId === prunedWindowId);
    expect(prunedHits).toHaveLength(settledCount);
    await expect(card).toBeVisible();

    console.log("M10_EDGES_EVIDENCE:" + JSON.stringify({
      case: "pruned_window_404",
      sessionId,
      prunedWindowId,
      prunedWindowRequestCount: prunedHits.length,
      status: initialHits[0]?.status ?? null,
      errorCode: initialHits[0]?.errorCode ?? null,
      quietWindowMs: 3000,
    }));
  });
});
