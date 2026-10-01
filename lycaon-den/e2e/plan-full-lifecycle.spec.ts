import { expect, test, type Page } from "@playwright/test";
import {
  PLAN_STUB_VALID_CONTENT,
  activateProject,
  apiApprovePlan,
  apiConfig,
  apiFindHarnessProject,
  apiFireWorkflowTransition,
  apiGetWorkflowRun,
  apiTryGetActiveWorkflowRun,
  apiJson,
  apiStartWorkflowRun,
  apiUpdatePlanContent,
  liveChatStage,
  openNewChatSession,
  webE2e,
} from "./helpers.ts";

import { manualHarnessTools } from "./manual-harness-tools.ts";

/** Applies the harness text scale; no argument locks the accessibility maximum. */
async function setTextScale(page: Page, scale?: number): Promise<number> {
  return page.evaluate((value) => {
    const harness = (
      window as unknown as {
        __harness?: { setTextScale(scale?: number): { textScale: number } };
      }
    ).__harness;
    return harness?.setTextScale(value).textScale ?? 53 / 17;
  }, scale);
}

/** Exercises plan writing, approval, critique, and execution with API-driven setup. */
webE2e.describe("plan full lifecycle", () => {
  webE2e("/plan expand → approve → execute", async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await page.waitForFunction(
      () =>
        (window as unknown as { __harness?: { state(): { connected: boolean } } }).__harness?.state()
          .connected === true,
      undefined,
      { timeout: 60_000 },
    );

    const project = await apiFindHarnessProject(request);
    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    let run = await apiStartWorkflowRun(request, sessionId, {
      workflow_id: "plan",
      workflow_version: "1.0.0",
      request: "Plan a ready endpoint for the fixture project.",
      parameters: { research_depth: "none" },
    });
    expect(run.workflow_id).toBe("plan");
    // Research depth none starts directly in plan writing.
    const planRunId = run.id;
    expect(run.current_phase).toBe("expand");
    expect(run.blueprint_path).toBeTruthy();
    let blueprintPath = run.blueprint_path!;

    await apiUpdatePlanContent(
      request,
      project.id,
      blueprintPath,
      PLAN_STUB_VALID_CONTENT,
    );
    // The bound-file write advances expand to approval asynchronously.
    await expect
      .poll(async () => (await apiGetWorkflowRun(request, planRunId)).current_phase, {
        timeout: 30_000,
      })
      .toBe("approve");
    run = await apiGetWorkflowRun(request, planRunId);
    expect(run.blueprint_path).toBeTruthy();
    blueprintPath = run.blueprint_path!;
    expect(run.ui?.choice_transitions?.some((t) => t.id === "critique")).toBeTruthy();

    // Approval controls remain reachable at maximum text scale.
    await page.setViewportSize({ width: 720, height: 480 });
    expect(await setTextScale(page)).toBeCloseTo(53 / 17, 3);

    const chatStage = liveChatStage(page);
    const blueprintCard = chatStage.getByTestId("blueprint-card");
    await expect(blueprintCard).toBeVisible({ timeout: 15_000 });
    // The blueprint shows itself when it starts asking — no click needed.
    const modal = chatStage.getByTestId("blueprint-review-modal");
    await expect(modal).toBeVisible({ timeout: 15_000 });
    await expect(modal.getByTestId("files-editor-preview")).not.toContainText(
      "status: draft",
    );
    for (const viewport of [
      { width: 720, height: 480, scale: 53 / 17 },
      { width: 480, height: 640, scale: 53 / 17 },
      { width: 1280, height: 900, scale: 1 },
    ]) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await setTextScale(page, viewport.scale);
      const footer = modal.locator(".den-blueprint-workspace-modal-footer");
      await expect.poll(async () => footer.evaluate((el) => {
        const modal = el.closest('[data-testid="blueprint-review-modal"]')!;
        return el.getBoundingClientRect().bottom <= modal.getBoundingClientRect().bottom + 1
          && el.scrollWidth <= el.clientWidth + 1;
      }), { message: `Footer remains inside ${viewport.width}×${viewport.height} at text scale ${viewport.scale}` }).toBe(true);
      const modalApprove = modal.getByTestId("blueprint-review-approve");
      await modalApprove.scrollIntoViewIfNeeded();
      await footer.evaluate((el) => { el.scrollTop = el.scrollHeight; });
      await expect.poll(async () => modalApprove.evaluate((el) => {
        const modal = el.closest('[data-testid="blueprint-review-modal"]')!;
        const button = el.getBoundingClientRect();
        const bounds = modal.getBoundingClientRect();
        return button.bottom <= bounds.bottom - 8 && button.top >= bounds.top;
      })).toBe(true);
    }

    // The blueprint contains the body. It needs room: the cramped viewport leaves no
    // free space for the column to hand a hidden sibling. Restored after.
    await page.setViewportSize({ width: 1280, height: 900 });
    await setTextScale(page, 1);
    const previewShare = await modal.evaluate((el) => {
      const editor = el.querySelector(".den-files-editor");
      const preview = el.querySelector('[data-testid="files-editor-preview"]');
      if (!editor || !preview) return Number.NaN;
      return preview.getBoundingClientRect().height / editor.getBoundingClientRect().height;
    });
    expect(previewShare).toBeGreaterThan(0.7);
    await page.setViewportSize({ width: 720, height: 480 });
    await setTextScale(page);

    // Open in files transfers the buffer into a foreground Files stage.
    await modal.getByTestId("blueprint-review-open-files").click();
    await expect(modal).toBeHidden();
    const filesStage = page.getByTestId("project-files-view");
    await expect(filesStage).toBeVisible();
    await expect(filesStage).toHaveClass(/project-files-view--tree-collapsed/);
    expect(
      await filesStage
        .locator(".den-files-editor-pane")
        .evaluate((element) => element.getBoundingClientRect().width),
    ).toBeGreaterThan(320);
    await expect(filesStage.getByTestId("files-editor-crumb")).toContainText(
      blueprintPath.split("/").pop() ?? blueprintPath,
    );
    await expect(page.getByTestId("shell-stage-host")).not.toHaveClass(
      /den-shell-stage-host--split/,
    );

    await page.setViewportSize({ width: 1280, height: 900 });
    await setTextScale(page, 1);
    await page.locator(`[data-session-id="${sessionId}"]`).first().click();
    const returnedChatStage = liveChatStage(page);
    const returnedCard = returnedChatStage.getByTestId("blueprint-card");
    await expect(returnedCard).toBeVisible();
    // The ask is a transcript row, not dock chrome and not a strip on the composer.
    expect(
      await returnedCard.evaluate((el) => ({
        inStream: Boolean(el.closest('[data-testid="chat-stream"]')),
        inTopDock: Boolean(el.closest(".den-chat-top-dock")),
        inComposerDock: Boolean(el.closest(".den-chat-composer-dock")),
      })),
    ).toEqual({ inStream: true, inTopDock: false, inComposerDock: false });
    // Five controls at max text scale stack instead of widening the transcript.
    const cardWidths = await returnedCard.evaluate((el) => ({
      client: el.clientWidth,
      scroll: el.scrollWidth,
    }));
    expect(cardWidths.scroll).toBeLessThanOrEqual(cardWidths.client + 1);
    await expect(returnedCard.getByTestId("blueprint-approve")).toBeVisible();

    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
    try {
      run = await apiFireWorkflowTransition(request, run.id, "critique");
      expect(run.current_phase).toBe("review");
      const driver = manualHarnessTools(request, sessionId);
      const read = await driver.invoke("read", { path: blueprintPath });
      await driver.completed(read);
      const verdict = await driver.invoke("submit_verdict", {
        verdict: { verdict: "APPROVED", bullets: "Scope matches the fixture endpoint." },
        cited_evidence: [{ path: blueprintPath, line: 1 }],
      });
      await driver.completed(verdict);
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Harness reply." });
    }
    await expect
      .poll(async () => (await apiGetWorkflowRun(request, planRunId)).current_phase, {
        timeout: 30_000,
      })
      .toBe("approve");

    await apiApprovePlan(request, project.id, blueprintPath, planRunId);

    await expect
      .poll(async () => {
        const active = await apiTryGetActiveWorkflowRun(request, sessionId);
        return active?.workflow_id === "implement" && active.current_phase === "work"
          ? active
          : null;
      }, { timeout: 60_000 })
      .toBeTruthy();

    const planRun = await apiGetWorkflowRun(request, planRunId);
    expect(planRun.current_phase).toBe("execute");

    const decidedCard = liveChatStage(page).getByTestId("blueprint-card");
    await expect(decidedCard.getByTestId("blueprint-card-decision")).toContainText(
      "Approved",
      { timeout: 30_000 },
    );
    await expect(decidedCard.getByTestId("blueprint-approve")).toHaveCount(0);

    const { apiUrl, token } = apiConfig();
    await expect
      .poll(async () => {
        const res = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!res.ok()) return false;
        const body = (await res.json()) as
          | Array<{ kind?: string }>
          | { messages?: Array<{ kind?: string }> };
        const msgs = Array.isArray(body) ? body : (body.messages ?? []);
        // The supplied initial request needs no separate intake question.
        return msgs.some((m) => m.kind === "blueprint");
      }, { timeout: 15_000 })
      .toBeTruthy();
  });
});
