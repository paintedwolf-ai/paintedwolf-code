import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import crypto from "node:crypto";
import { expect, type Locator } from "@playwright/test";
import type { Message } from "../src/api/types.ts";
import {
  activateProject,
  apiJson,
  apiPostPrompt,
  bootstrapChatSession,
  E2E_FIXTURE_PROJECT,
  expectShellReady,
  liveChatStage,
  openSessionRow,
  settledChatSessionId,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import { manualHarnessTools, shellWord } from "./manual-harness-tools.ts";

type PendingCheckpoint = {
  id: string;
  kind: string;
  status: string;
  tool_approval?: { tool_call_id?: string; plan?: { subject?: { kind?: string } } };
};

async function expandDisclosure(container: Locator) {
  await expect(container).toBeVisible({ timeout: 30_000 });
  if ((await container.getAttribute("data-expanded")) !== "true") {
    await container.locator(":scope > .den-transcript-disclosure-header").click();
  }
  await expect(container).toHaveAttribute("data-expanded", "true");
}

webE2e("M12 Den recovery: rendered approval-no denies outside write_root, allowed alternative succeeds, and receipt survives reload", async ({ page, request }) => {
  webE2e.setTimeout(180_000);

  const project = await bootstrapChatSession(page, request);
  const projectDir = E2E_FIXTURE_PROJECT;
  const sessionId = await settledChatSessionId(page);
  const outsideDir = fs.mkdtempSync(path.join(os.tmpdir(), "pw-m12-den-outside-"));
  const outsideTarget = path.join(outsideDir, "denied.txt");
  const suffix = crypto.randomUUID().slice(0, 10);
  const left = "m12-allowed-";
  const expectedMarker = `${left}${suffix}`;
  const allowedRel = `m12-file-${suffix}.txt`;
  const allowedFull = path.join(projectDir!, allowedRel);

  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });

  try {
    const prompt = await apiPostPrompt(request, sessionId, {
      text: "Attempt outside write_root approval, respect Den rejection, and complete the allowed in-project alternative.",
    });
    expect(prompt.ok(), await prompt.text()).toBe(true);

    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("update_progress");
    const progRaw = await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [ ] Handle outside write-root refusal and write allowed in-project file\n",
    }));
    const progSnap = (JSON.parse(progRaw) as { snapshot: { revision: string; item_ids: string[] } }).snapshot;

    // 1. Trigger a real write_root approval checkpoint for an outside-project path.
    const refusalCommand = `external -- /bin/sh -c ${shellWord(`printf 'denied\\n' > ${shellWord(outsideTarget)}`)}`;
    const deniedCallId = await driver.invoke("command", {
      command: refusalCommand,
      capability_request: { write_root: outsideDir },
    });

    let checkpoint: PendingCheckpoint | undefined;
    await expect.poll(async () => {
      const resp = await apiJson<{ checkpoints: PendingCheckpoint[] }>(
        request,
        "GET",
        `/v1/sessions/${sessionId}/checkpoints?status=pending`,
      );
      checkpoint = (resp.checkpoints ?? []).find(
        (cp) => cp.kind === "tool_approval" && cp.tool_approval?.tool_call_id === deniedCallId,
      );
      return checkpoint?.id ?? "";
    }, { timeout: 45_000 }).not.toBe("");

    const checkpointId = checkpoint!.id;
    expect(fs.existsSync(outsideTarget)).toBe(false);

    // 2. Reject via the ACTUAL rendered Den approval-no control scoped to the matching checkpoint.
    const approvalCard = page.locator(`[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`);
    await expect(approvalCard).toBeVisible({ timeout: 30_000 });
    const noBtn = approvalCard.getByTestId("approval-no");
    await expect(noBtn).toBeVisible();
    await noBtn.click();

    // 3. Confirm the denied tool result settled as rejected with SANDBOX_WRITE_ROOT_DENIED and zero outside writes.
    let deniedResult: { outcome?: string; content?: string } | undefined;
    await expect.poll(async () => {
      const body = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`);
      deniedResult = body.messages.find((m) => m.tool_result?.tool_call_id === deniedCallId)?.tool_result;
      return deniedResult?.outcome ?? "";
    }, { timeout: 45_000 }).toBe("rejected");
    expect(String(deniedResult?.content ?? "")).toContain("SANDBOX_WRITE_ROOT_DENIED");
    expect(fs.existsSync(outsideTarget)).toBe(false);

    // 4. Perform the allowed in-project alternative in the same session (runtime-generated marker).
    const allowedCmd = `printf '%s%s\\n' '${left}' '${suffix}' > ./${allowedRel} && cat ./${allowedRel}`;
    expect(allowedCmd.includes(expectedMarker)).toBe(false);
    const allowedCallId = await driver.invoke("command", { command: allowedCmd });
    const allowedContent = await driver.completed(allowedCallId);
    expect(allowedContent).toContain(expectedMarker);

    const midMessages = await apiJson<{ messages: Message[] }>(request, "GET", `/v1/sessions/${sessionId}/messages`);
    const allowedToolResult = midMessages.messages.find((m) => m.tool_result?.tool_call_id === allowedCallId)?.tool_result;
    const allowedProgramId = (allowedToolResult?.command_program as { id?: string; state?: string } | undefined)?.id ?? "";
    const allowedProgramState = (allowedToolResult?.command_program as { id?: string; state?: string } | undefined)?.state ?? "";
    expect(allowedProgramId).not.toBe("");
    expect(allowedProgramState).toBe("completed");
    expect(fs.existsSync(allowedFull)).toBe(true);
    expect(fs.readFileSync(allowedFull, "utf8")).toBe(`${expectedMarker}\n`);
    expect(fs.existsSync(outsideTarget)).toBe(false);

    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [x] Handle outside write-root refusal and write allowed in-project file\n",
      expected_revision: progSnap.revision,
      item_ids: progSnap.item_ids,
    }));

    const finalPending = await apiJson<{ pending: boolean; id: string }>(
      request,
      "GET",
      `/harness/llm/pending?wait=30000&session_id=${sessionId}`,
    );
    expect(finalPending.pending).toBe(true);
    await apiJson(request, "POST", "/harness/llm/respond", {
      id: finalPending.id,
      content: "M12 Den denial and allowed alternative verified.",
      tool_calls: [],
    });

    await expect(liveChatStage(page).getByTestId("transcript-article-assistant").last()).toContainText(
      "M12 Den denial and allowed alternative verified.",
      { timeout: 30_000 },
    );

    // 5. Full reload & first reopen: verify allowed alternative's exact card, receipt, and runtime output survive.
    await page.reload();
    await waitHarnessConnected(page);
    await expectShellReady(page);
    if (await page.getByTestId("home-nav").isVisible().catch(() => false)) {
      await activateProject(page, project.id);
    }
    const reopenedSession = await settledChatSessionId(page).catch(() => "");
    if (reopenedSession !== sessionId) {
      await openSessionRow(page, sessionId);
    }
    await expect.poll(() => settledChatSessionId(page), { timeout: 30_000 }).toBe(sessionId);

    const stream = liveChatStage(page).getByTestId("chat-stream").first();
    const spans = stream.getByTestId("activity-span-card");
    const spanCount = await spans.count();
    for (let i = 0; i < spanCount; i++) {
      await expandDisclosure(spans.nth(i));
    }

    const allowedCard = stream.locator(
      `[data-testid="action-card"][data-tool-call-id="${allowedCallId}"][data-status="done"]:not([data-tool-part-id*=":activity:"])`,
    );
    await expect(allowedCard).toBeVisible({ timeout: 30_000 });
    await expandDisclosure(allowedCard);
    const outputLink = allowedCard.getByTestId("action-link-output");
    await expect(outputLink).toBeVisible({ timeout: 15_000 });
    await outputLink.click();
    const contentPage = page.getByTestId("chat-content-page");
    await expect(contentPage).toBeVisible({ timeout: 15_000 });
    await expect(contentPage).toContainText(expectedMarker, { timeout: 15_000 });

    const evidence = {
      case: "M12-den-recovery",
      projectId: project.id,
      sessionId,
      checkpointId,
      deniedCallId,
      deniedOutcome: deniedResult?.outcome ?? "",
      deniedCode: "SANDBOX_WRITE_ROOT_DENIED",
      outsideExistsFinal: fs.existsSync(outsideTarget),
      allowedCallId,
      allowedProgramId,
      allowedProgramState,
      allowedMarker: expectedMarker,
      markerAbsentFromSource: !allowedCmd.includes(expectedMarker),
      allowedDiskMatches: fs.readFileSync(allowedFull, "utf8") === `${expectedMarker}\n`,
      reopenedOutputVerified: true,
    };
    console.log(`M12_DEN_RECOVERY_EVIDENCE:${JSON.stringify(evidence)}`);
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true }).catch(() => {});
    fs.rmSync(outsideDir, { recursive: true, force: true });
    fs.rmSync(allowedFull, { force: true });
  }
});
