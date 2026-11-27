import { execSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test, type APIRequestContext, type Locator, type Page, type Response } from "@playwright/test";
import type { CheckpointListResponse, CommandProgramReceiptPage, CommandReceiptDocumentPage, Message } from "../src/api/types.ts";
import {
  activateProject,
  apiConfig,
  apiJson,
  apiPostPrompt,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  openSessionRow,
  seedAppState,
  settledChatSessionId,
  waitForSessionReady,
} from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

type Phase = "live" | "settled" | "reload";
type PendingTurn = { pending: boolean; id: string; session_id: string };
type ReceiptRecord = { phase: Phase; status: number; limit: string | null; state: string | null; activityRevision: number | null; executionsLength: number; activitiesLength: number };
type DocumentRecord = { phase: Phase; status: number; revision: string | null; settled: boolean | null; rowsLength: number; totalRows: number; complete: boolean | null };
type WindowRecord = { phase: Phase; status: number; windowId: string; errorCode: string | null };

const scriptsDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../scripts");

function runScript(name: string, envExtra: Record<string, string>, ...args: string[]) {
  execSync(`bash ${path.join(scriptsDir, name)} ${args.join(" ")}`.trimEnd(), {
    stdio: "inherit",
    env: { ...process.env, ...envExtra },
  });
}

async function ensureManualLLM(request: APIRequestContext) {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const probe = await request.post(`${apiUrl}/harness/llm/auto`, { headers, data: { enabled: true, text: "Done." } }).catch(() => undefined);
  if (probe?.ok()) return;
  runScript("e2e-sidecar-stop.sh", {});
  runScript("e2e-sidecar-bg.sh", { LYCAON_LLM_MANUAL: "1", LYCAON_LLM_MOCK: "1" }, "--reuse-built");
  await expect.poll(async () => {
    const health = await request.get(`${apiUrl}/health`).catch(() => undefined);
    return health?.ok() ?? false;
  }, { timeout: 60_000 }).toBe(true);
  const retry = await request.post(`${apiUrl}/harness/llm/auto`, { headers, data: { enabled: true, text: "Done." } });
  expect(retry.ok(), await retry.text()).toBeTruthy();
}

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
      let body: Partial<CommandProgramReceiptPage> | undefined;
      try { body = (await response.json()) as Partial<CommandProgramReceiptPage>; } catch { /* non-JSON */ }
      receipts.push({
        phase: currentPhase,
        status,
        limit: parsed.searchParams.get("limit"),
        state: body?.program?.state ?? null,
        activityRevision: body?.program?.activity_revision ?? null,
        executionsLength: body?.executions?.length ?? 0,
        activitiesLength: body?.activities?.length ?? 0,
      });
      return;
    }

    if (parsed.pathname.endsWith("/program-receipt-document")) {
      let body: Partial<CommandReceiptDocumentPage> | undefined;
      try { body = (await response.json()) as Partial<CommandReceiptDocumentPage>; } catch { /* non-JSON */ }
      documents.push({
        phase: currentPhase,
        status,
        revision: body?.revision ?? null,
        settled: body?.settled ?? null,
        rowsLength: body?.rows?.length ?? 0,
        totalRows: body?.total_rows ?? 0,
        complete: body?.complete ?? null,
      });
      return;
    }

    if (parsed.pathname.includes("/source/command-windows/")) {
      const windowId = parsed.pathname.split("/source/command-windows/")[1]?.split("/")[0] ?? "";
      let body: { code?: string } | undefined;
      try { body = (await response.json()) as { code?: string }; } catch { /* non-JSON */ }
      windows.push({ phase: currentPhase, status, windowId, errorCode: body?.code ?? null });
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

async function ensureChatView(page: Page) {
  const backToChat = page.getByRole("button", { name: "Back to chat" });
  if (await backToChat.isVisible().catch(() => false)) {
    await backToChat.click();
  }
}

async function expandDisclosure(container: Locator) {
  await expect(container).toBeVisible({ timeout: 30_000 });
  if ((await container.getAttribute("data-expanded")) !== "true") {
    await container.locator(":scope > .den-transcript-disclosure-header").click();
  }
  await expect(container).toHaveAttribute("data-expanded", "true");
}

async function openFoldedCommand(page: Page) {
  await ensureChatView(page);
  const span = liveChatStage(page).getByTestId("activity-span-card").last();
  await expandDisclosure(span);
  const card = span.getByTestId("action-card").first();
  await expandDisclosure(card);
  return { span, card };
}

async function reloadToSession(page: Page, projectId: string, sessionId: string) {
  await page.reload();
  await expect(page.getByTestId("shell")).toHaveAttribute("data-sidecar-status", "connected", { timeout: 60_000 });
  await expectShellReady(page);
  if (await page.getByTestId("home-nav").isVisible().catch(() => false)) {
    await activateProject(page, projectId);
  }
  const current = await settledChatSessionId(page).catch(() => "");
  if (current !== sessionId) {
    await openSessionRow(page, sessionId);
  }
  await expect.poll(() => settledChatSessionId(page), { timeout: 30_000 }).toBe(sessionId);
}

test.describe("portable command desktop smoke", () => {
  test("M10: portable command live-to-settled receipts, document pages, reload persistence, and bounded L01/L07 counts", async ({ page, request }, testInfo) => {
    test.setTimeout(180_000);
    await ensureManualLLM(request);
    const recorder = attachEvidenceRecorder(page);

    await seedAppState(page);
    const project = await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });

    try {
      const prompt = await apiPostPrompt(request, sessionId, {
        text: "Run a multi-step portable command in desktop Den and report the filtered line.",
      });
      expect(prompt.ok(), await prompt.text()).toBe(true);

      const driver = manualHarnessTools(request, sessionId);
      const commandText = "printf 'alpha\\nbeta\\n' | grep alpha && sleep 2 && ls && printf 'm10-desktop-settled\\n'";
      const callId = await driver.invoke("command", { command: commandText });

      const liveSpan = liveChatStage(page).getByTestId("activity-span-card").last();
      if (await liveSpan.isVisible({ timeout: 3_000 }).catch(() => false)) {
        const { card: liveCard } = await openFoldedCommand(page);
        const liveReceipt = liveCard.getByTestId("action-link-receipt");
        if (await liveReceipt.isVisible().catch(() => false)) {
          await liveReceipt.click();
        }
      }

      const toolOutput = await driver.completed(callId);
      expect(toolOutput).toContain("alpha");

      const { card: midCard } = await openFoldedCommand(page);
      const midReceiptLink = midCard.getByTestId("action-link-receipt");
      await expect(midReceiptLink).toBeVisible({ timeout: 15_000 });
      await midReceiptLink.click();

      const pending = await apiJson<PendingTurn>(request, "GET", `/harness/llm/pending?wait=30000&session_id=${sessionId}`);
      expect(pending.pending).toBe(true);
      recorder.setPhase("settled");
      await apiJson(request, "POST", "/harness/llm/respond", {
        id: pending.id,
        content: "Desktop portable command completed: alpha.",
      });
      await waitForSessionReady(request, sessionId);
      await ensureChatView(page);
      await expect(liveChatStage(page).getByTestId("transcript-article-assistant").last()).toContainText("Desktop portable command completed: alpha.");

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

      await page.waitForTimeout(1_000);
      await recorder.drain();

      recorder.setPhase("reload");
      await reloadToSession(page, project.id, sessionId);

      const { card: reloadedCard } = await openFoldedCommand(page);
      await expect(reloadedCard).toContainText("executions", { timeout: 15_000 });
      const reloadedReceiptLink = reloadedCard.getByTestId("action-link-receipt");
      await expect(reloadedReceiptLink).toBeVisible({ timeout: 15_000 });
      await reloadedReceiptLink.click();

      await expect.poll(async () => {
        await recorder.drain();
        return recorder.documents.some((entry) => entry.phase === "reload" && entry.status === 200 && entry.settled === true && entry.rowsLength > 0);
      }, { timeout: 20_000 }).toBe(true);

      await ensureChatView(page);
      await reloadedCard.locator(":scope > .den-transcript-disclosure-header").click();
      await expect(reloadedCard).toHaveAttribute("data-expanded", "false");
      await expandDisclosure(reloadedCard);
      await page.waitForTimeout(1_000);
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
        pruned404ByWindow: Object.fromEntries(pruned404ByWindow),
        receiptRevisions,
        documentRevisions: recorder.documents.filter((e) => e.status === 200).map((e) => ({
          phase: e.phase, revision: e.revision, settled: e.settled, rowsLength: e.rowsLength, totalRows: e.totalRows, complete: e.complete,
        })),
      };
      console.log("M10_DESKTOP_EVIDENCE:" + JSON.stringify(evidence));
    } catch (error) {
      const transcript = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`).catch(() => undefined);
      await testInfo.attach("m10-desktop-failure", {
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

  test("M12: pending command tool approval checkpoint survives reload and clears cleanly when approved", async ({ page, request }) => {
    test.setTimeout(180_000);
    await seedAppState(page);
    const project = await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const { apiUrl, token } = apiConfig();
    const headers = { Authorization: `Bearer ${token}` };

    const created = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers,
      data: { session_id: sessionId, command: "printf 'm12 desktop fixture'" },
    });
    expect(created.ok(), await created.text()).toBeTruthy();
    const { checkpoint_id: checkpointId } = (await created.json()) as { checkpoint_id: string };
    const card = () => liveChatStage(page).locator(`[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`);
    await expect(card()).toBeVisible({ timeout: 30_000 });

    await reloadToSession(page, project.id, sessionId);
    await expect(card()).toBeVisible({ timeout: 30_000 });

    const pendingRes = await request.get(`${apiUrl}/v1/sessions/${sessionId}/checkpoints?include_children=true`, { headers });
    expect(pendingRes.ok()).toBeTruthy();
    expect(((await pendingRes.json()) as CheckpointListResponse).checkpoints.map((cp) => cp.id)).toEqual([checkpointId]);

    const attentionRes = await request.get(`${apiUrl}/v1/attention`, { headers });
    expect(attentionRes.ok()).toBeTruthy();
    expect((await attentionRes.json()).rows).toEqual(expect.arrayContaining([
      expect.objectContaining({ session_id: sessionId, class: "needs_you", reason: "checkpoint" }),
    ]));

    await card().getByTestId("approval-approve-primary").click();
    await expect(card()).toHaveCount(0, { timeout: 30_000 });

    await reloadToSession(page, project.id, sessionId);
    await expect(card()).toHaveCount(0);

    const bootstrapRes = await request.get(`${apiUrl}/v1/sessions/${sessionId}/bootstrap`, { headers });
    expect(bootstrapRes.ok()).toBeTruthy();
    expect((await bootstrapRes.json()).checkpoints).toEqual([]);

    console.log("M12_DESKTOP_EVIDENCE:" + JSON.stringify({ sessionId, checkpointId, survivedReload: true, clearedAfterApproveAndReload: true }));
  });
});
