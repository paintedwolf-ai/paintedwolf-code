import { expect, type Locator, type Page, type Response } from "@playwright/test";
import type { CommandProgramReceiptPage, CommandReceiptDocumentPage, Message } from "../src/api/types.ts";
import {
  apiJson,
  apiPostPrompt,
  bootstrapChatSession,
  liveChatStage,
  openSessionRow,
  settledChatSessionId,
  waitForSessionReady,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

type Phase = "live" | "settled" | "reload";

type ReceiptRecord = {
  phase: Phase;
  url: string;
  status: number;
  programId: string;
  limit: string | null;
  cursor: string | null;
  activityId: string | null;
  state: string | null;
  activityRevision: number | null;
  executionsLength: number;
  executionsOmitted: number;
  nextCursor: string | null;
  activitiesLength: number;
};

type DocumentRecord = {
  phase: Phase;
  url: string;
  status: number;
  scopes: string[];
  offset: string | null;
  limit: string | null;
  revisionParam: string | null;
  revision: string | null;
  totalRows: number;
  settled: boolean | null;
  endOffset: number;
  complete: boolean | null;
  rowsLength: number;
};

type WindowRecord = {
  phase: Phase;
  url: string;
  status: number;
  windowId: string;
  settling: boolean | null;
  coverageLength: number;
  errorCode: string | null;
};

type PendingTurn = { pending: boolean; id: string; session_id: string; tools?: string[] };

function attachEvidenceRecorder(page: Page) {
  let phase: Phase = "live";
  const receipts: ReceiptRecord[] = [];
  const documents: DocumentRecord[] = [];
  const windows: WindowRecord[] = [];
  const inFlight = new Set<Promise<void>>();

  const record = async (response: Response) => {
    const parsed = new URL(response.url());
    const status = response.status();
    const currentPhase = phase;

    if (parsed.pathname.includes("/program-receipts/")) {
      const programId = parsed.pathname.split("/program-receipts/")[1]?.split("/")[0] ?? "";
      let body: Partial<CommandProgramReceiptPage> | undefined;
      try {
        body = (await response.json()) as Partial<CommandProgramReceiptPage>;
      } catch {
        // Non-JSON error response.
      }
      receipts.push({
        phase: currentPhase,
        url: parsed.pathname + parsed.search,
        status,
        programId,
        limit: parsed.searchParams.get("limit"),
        cursor: parsed.searchParams.get("cursor"),
        activityId: parsed.searchParams.get("activity_id"),
        state: body?.program?.state ?? null,
        activityRevision: body?.program?.activity_revision ?? null,
        executionsLength: body?.executions?.length ?? 0,
        executionsOmitted: body?.executions_omitted ?? 0,
        nextCursor: body?.next_cursor ?? null,
        activitiesLength: body?.activities?.length ?? 0,
      });
      return;
    }

    if (parsed.pathname.endsWith("/program-receipt-document")) {
      let body: Partial<CommandReceiptDocumentPage> | undefined;
      try {
        body = (await response.json()) as Partial<CommandReceiptDocumentPage>;
      } catch {
        // Non-JSON error response.
      }
      documents.push({
        phase: currentPhase,
        url: parsed.pathname + parsed.search,
        status,
        scopes: parsed.searchParams.getAll("scope"),
        offset: parsed.searchParams.get("offset"),
        limit: parsed.searchParams.get("limit"),
        revisionParam: parsed.searchParams.get("revision"),
        revision: body?.revision ?? null,
        totalRows: body?.total_rows ?? 0,
        settled: body?.settled ?? null,
        endOffset: body?.end_offset ?? 0,
        complete: body?.complete ?? null,
        rowsLength: body?.rows?.length ?? 0,
      });
      return;
    }

    if (parsed.pathname.includes("/source/command-windows/")) {
      const windowId = parsed.pathname.split("/source/command-windows/")[1]?.split("/")[0] ?? "";
      let body: { settling?: boolean; coverage?: unknown[]; code?: string } | undefined;
      try {
        body = (await response.json()) as { settling?: boolean; coverage?: unknown[]; code?: string };
      } catch {
        // Non-JSON error response.
      }
      windows.push({
        phase: currentPhase,
        url: parsed.pathname + parsed.search,
        status,
        windowId,
        settling: body?.settling ?? null,
        coverageLength: body?.coverage?.length ?? 0,
        errorCode: body?.code ?? null,
      });
    }
  };

  page.on("response", (response) => {
    const task = record(response).finally(() => { inFlight.delete(task); });
    inFlight.add(task);
  });

  return {
    setPhase(next: Phase) { phase = next; },
    receipts,
    documents,
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

async function ensureChatView(page: Page) {
  const backToChat = page.getByRole("button", { name: "Back to chat" });
  if (await backToChat.isVisible().catch(() => false)) {
    await backToChat.click();
  }
}

async function openFoldedCommand(page: Page) {
  await ensureChatView(page);
  const span = liveChatStage(page).getByTestId("activity-span-card").last();
  await expandDisclosure(span);
  const card = span.getByTestId("action-card").first();
  await expandDisclosure(card);
  return { span, card };
}

webE2e("captures live and settled portable command receipts, document pages, reload persistence, and bounded L01/L07 counts", async ({ page, request }, testInfo) => {
  webE2e.setTimeout(180_000);
  const recorder = attachEvidenceRecorder(page);

  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });

  try {
    const prompt = await apiPostPrompt(request, sessionId, {
      text: "Run a multi-step portable command and report the filtered line.",
    });
    expect(prompt.ok(), await prompt.text()).toBe(true);

    const driver = manualHarnessTools(request, sessionId);
    const commandText = "printf 'alpha\\nbeta\\n' | grep alpha && sleep 2 && ls && printf 'm10-settled\\n'";
    const callId = await driver.invoke("command", { command: commandText });

    // If Den surfaces the live activity span while sleep is running, open it early to capture live requests.
    const liveSpan = liveChatStage(page).getByTestId("activity-span-card").last();
    if (await liveSpan.isVisible({ timeout: 3_000 }).catch(() => false)) {
      const { card: liveCard } = await openFoldedCommand(page);
      const liveReceipt = liveCard.getByTestId("action-link-receipt");
      if (await liveReceipt.isVisible().catch(() => false)) {
        await liveReceipt.click();
      }
    }

    // Confirm the real portable command completed cleanly.
    const toolOutput = await driver.completed(callId);
    expect(toolOutput).toContain("alpha");

    // Open the folded command activity while the turn is still open, then settle the assistant turn.
    const { card: midCard } = await openFoldedCommand(page);
    const midReceiptLink = midCard.getByTestId("action-link-receipt");
    await expect(midReceiptLink).toBeVisible({ timeout: 15_000 });
    await midReceiptLink.click();

    const pending = await apiJson<PendingTurn>(request, "GET", `/harness/llm/pending?wait=30000&session_id=${sessionId}`);
    expect(pending.pending).toBe(true);
    recorder.setPhase("settled");
    await apiJson(request, "POST", "/harness/llm/respond", {
      id: pending.id,
      content: "Portable command completed: alpha.",
    });
    await waitForSessionReady(request, sessionId);
    await ensureChatView(page);
    await expect(liveChatStage(page).getByTestId("transcript-article-assistant").last()).toContainText("Portable command completed: alpha.");

    const { card: settledCard } = await openFoldedCommand(page);
    await expect(settledCard).toContainText("executions", { timeout: 15_000 });
    const settledReceiptLink = settledCard.getByTestId("action-link-receipt");
    await expect(settledReceiptLink).toBeVisible({ timeout: 15_000 });
    await settledReceiptLink.click();

    await expect.poll(async () => {
      await recorder.drain();
      return recorder.receipts.some((entry) => entry.status === 200 && entry.state === "completed" && entry.executionsLength > 0);
    }, { timeout: 20_000 }).toBe(true);

    await expect.poll(async () => {
      await recorder.drain();
      return recorder.documents.some((entry) => entry.status === 200 && entry.settled === true && entry.rowsLength > 0);
    }, { timeout: 20_000 }).toBe(true);

    // Confirm settled receipt viewer is rendered with no unsettled refresh button.
    await expect(page.getByTestId("chat-content-page")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("chat-process-pretty-refresh")).toHaveCount(0);
    await recorder.drain();

    // Reload Den and verify folded activity and receipt document survive with bounded requests.
    recorder.setPhase("reload");
    await page.reload();
    await waitHarnessConnected(page);
    const afterReloadSession = await settledChatSessionId(page).catch(() => "");
    if (afterReloadSession !== sessionId) {
      await openSessionRow(page, sessionId);
    }
    await expect.poll(() => settledChatSessionId(page)).toBe(sessionId);

    const { card: reloadedCard } = await openFoldedCommand(page);
    await expect(reloadedCard).toContainText("executions", { timeout: 15_000 });
    const reloadedReceiptLink = reloadedCard.getByTestId("action-link-receipt");
    await expect(reloadedReceiptLink).toBeVisible({ timeout: 15_000 });
    await reloadedReceiptLink.click();

    await expect.poll(async () => {
      await recorder.drain();
      return recorder.documents.some((entry) => entry.phase === "reload" && entry.status === 200 && entry.settled === true && entry.rowsLength > 0);
    }, { timeout: 20_000 }).toBe(true);

    // Close and reopen the settled card to verify L01 cache prevents redundant receipt refetches.
    await ensureChatView(page);
    await reloadedCard.locator(":scope > .den-transcript-disclosure-header").click();
    await expect(reloadedCard).toHaveAttribute("data-expanded", "false");
    await expandDisclosure(reloadedCard);
    await expect(reloadedCard).toContainText("executions", { timeout: 15_000 });
    await expect(reloadedCard.getByTestId("action-link-receipt")).toBeVisible({ timeout: 15_000 });
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    await recorder.drain();

    const receiptRevisions = recorder.receipts
      .filter((entry) => entry.status === 200 && entry.activityRevision !== null)
      .map((entry) => entry.activityRevision as number);
    for (let index = 1; index < receiptRevisions.length; index += 1) {
      expect(receiptRevisions[index]).toBeGreaterThanOrEqual(receiptRevisions[index - 1] ?? 0);
    }

    const settledDocs = recorder.documents.filter((entry) => entry.status === 200 && entry.settled === true);
    const reloadDocs = settledDocs.filter((entry) => entry.phase === "reload");
    expect(settledDocs.length).toBeGreaterThanOrEqual(1);
    expect(reloadDocs.length).toBeGreaterThanOrEqual(1);
    expect(reloadDocs[0]?.revision).toBe(settledDocs[0]?.revision);

    const reloadReceipts = recorder.receipts.filter((entry) => entry.phase === "reload" && entry.status === 200);
    expect(reloadReceipts.length).toBeGreaterThanOrEqual(1);
    expect(reloadReceipts.length).toBeLessThanOrEqual(6);

    const pruned404ByWindow = new Map<string, number>();
    for (const entry of recorder.windows) {
      if (entry.status === 404 && entry.errorCode === "source_history_not_found") {
        pruned404ByWindow.set(entry.windowId, (pruned404ByWindow.get(entry.windowId) ?? 0) + 1);
      }
    }
    for (const count of pruned404ByWindow.values()) {
      expect(count).toBeLessThanOrEqual(2);
    }

    const evidence = {
      sessionId,
      receiptCountsByPhase: {
        live: recorder.receipts.filter((e) => e.phase === "live").length,
        settled: recorder.receipts.filter((e) => e.phase === "settled").length,
        reload: recorder.receipts.filter((e) => e.phase === "reload").length,
      },
      documentCountsByPhase: {
        live: recorder.documents.filter((e) => e.phase === "live").length,
        settled: recorder.documents.filter((e) => e.phase === "settled").length,
        reload: recorder.documents.filter((e) => e.phase === "reload").length,
      },
      windowCountsByPhase: {
        live: recorder.windows.filter((e) => e.phase === "live").length,
        settled: recorder.windows.filter((e) => e.phase === "settled").length,
        reload: recorder.windows.filter((e) => e.phase === "reload").length,
      },
      pruned404ByWindow: Object.fromEntries(pruned404ByWindow),
      receiptRevisions,
      documentRevisions: recorder.documents.filter((e) => e.status === 200).map((e) => ({
        phase: e.phase,
        revision: e.revision,
        settled: e.settled,
        rowsLength: e.rowsLength,
        totalRows: e.totalRows,
        complete: e.complete,
      })),
      settledExecutionCounts: recorder.receipts
        .filter((e) => e.status === 200 && e.state === "completed")
        .map((e) => ({ phase: e.phase, limit: e.limit, executionsLength: e.executionsLength, activitiesLength: e.activitiesLength, activityRevision: e.activityRevision })),
    };
    console.log("M10_DEN_EVIDENCE:" + JSON.stringify(evidence));
  } catch (error) {
    const transcript = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`).catch(() => undefined);
    await testInfo.attach("m10-failure-state", {
      body: JSON.stringify({ transcript, receipts: recorder.receipts, documents: recorder.documents, windows: recorder.windows }, null, 2),
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
