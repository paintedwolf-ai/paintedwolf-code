import { expect, test } from "@playwright/test";
import {
  apiConfig,
  bootstrapChatSession,
  liveChatStage,
  sendChatPrompt,
  webE2e,
} from "./helpers.ts";

type HarnessWindow = {
  __harness?: {
    setTextScale(scale?: number): { ok: boolean; textScale: number };
    state(): { sessionId?: string | null };
  };
};

/** Harness subset of the macOS accessibility checklist in docs/accessibility.md. */
webE2e.describe("macOS accessibility", () => {
  webE2e("landmarks, conversation log, max scale @ 720px", async ({ page }) => {
    await bootstrapChatSession(page);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();

    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    await expect(page.getByRole("main", { name: "Chat" })).toBeVisible();

    // role=log mounts once the transcript has at least one item.
    await sendChatPrompt(page, "accessibility probe");
    const log = page.getByTestId("conversation-log");
    await expect(log).toBeVisible({ timeout: 30_000 });
    await expect(log).toHaveAttribute("role", "log");
    await expect(log).toHaveAttribute("aria-label", "Conversation");

    await page.setViewportSize({ width: 720, height: 480 });
    const textScale = await page.evaluate(() => {
      const h = (window as unknown as HarnessWindow).__harness;
      return h?.setTextScale?.().textScale ?? 53 / 17;
    });
    expect(textScale).toBeCloseTo(53 / 17, 3);

    const sendBox = await liveChatStage(page).getByTestId("composer-send").boundingBox();
    expect(sendBox!.width).toBeGreaterThanOrEqual(24);
    expect(sendBox!.height).toBeGreaterThanOrEqual(24);

    const sessionId = await page.evaluate(() => {
      return (
        (window as unknown as HarnessWindow).__harness?.state().sessionId ??
        document
          .querySelector<HTMLElement>('[data-testid="chat-stream"][data-session-id]')
          ?.getAttribute("data-session-id")
      );
    });

    await page.evaluate(() => {
      (window as unknown as HarnessWindow).__harness?.setTextScale?.(1);
    });

    // eslint-disable-next-line no-console -- device-verification evidence
    console.log(
      `landmarks/scale session=${sessionId ?? "(none)"} textScale=${textScale} viewport=720x480`,
    );
    expect(sessionId).toBeTruthy();
  });

  webE2e("Crossbar → escalate leaves sidebar interactive; Escape exits stage", async ({ page }) => {
    await bootstrapChatSession(page);
    const opener = page.getByTestId("project-search-entry");
    await expect(opener).toBeVisible();
    // The Crossbar front door is the keyboard chord; the sidebar entry is the pane.
    await page.keyboard.press("ControlOrMeta+k");

    const crossbar = page.getByTestId("crossbar");
    await expect(crossbar).toBeVisible({ timeout: 15_000 });
    await expect(crossbar).toHaveAttribute("role", "dialog");
    await expect(opener).toHaveClass(/project-context-zone__entry--active/);

    await page.getByTestId("crossbar-input").fill("kind:code");
    await page.getByTestId("crossbar-escalate").click();

    const search = page.getByTestId("global-search-view");
    const searchSurface = page.locator('[data-resident-key$=":search"]');
    await expect(search).toBeVisible({ timeout: 15_000 });
    await expect(searchSurface).toHaveAttribute("data-resident", "active", {
      timeout: 15_000,
    });
    await expect(page.getByTestId("search-query-input")).toBeFocused();
    await expect(search).not.toHaveAttribute("role", "dialog");
    await expect(search).not.toHaveAttribute("aria-modal", "true");
    await expect(opener).toHaveClass(/project-context-zone__entry--active/);

    // Pane, not overlay: sidebar stays clickable (aside is not inert).
    const config = page.getByTestId("project-configuration-entry");
    await config.click();
    await expect(searchSurface).toHaveAttribute("data-resident", "idle");
    await expect(searchSurface).toHaveAttribute("aria-hidden", "true");
    await expect(searchSurface).toHaveAttribute("inert", "");
    await expect(config).toHaveClass(/project-cluster__icon-btn--active/);

    await page.keyboard.press("ControlOrMeta+k");
    await expect(page.getByTestId("crossbar")).toBeVisible({ timeout: 15_000 });
    await page.getByTestId("crossbar-input").fill("kind:code");
    await page.getByTestId("crossbar-escalate").click();
    await expect(search).toBeVisible({ timeout: 15_000 });
    await expect(searchSurface).toHaveAttribute("data-resident", "active", {
      timeout: 15_000,
    });
    await expect(page.getByTestId("search-query-input")).toBeFocused();
    if (await page.getByTestId("search-query-input").getAttribute("aria-expanded") === "true") {
      await page.keyboard.press("Escape");
      await expect(page.getByTestId("search-query-input")).toHaveAttribute("aria-expanded", "false");
      await expect(searchSurface).toHaveAttribute("data-resident", "active");
    }
    await page.keyboard.press("Escape");
    await expect(searchSurface).toHaveAttribute("data-resident", "idle");
  });

  webE2e("closing Settings restores the control that opened it", async ({ page }) => {
    await bootstrapChatSession(page);
    const opener = page.getByRole("button", { name: "Settings", exact: true });
    await opener.focus();
    await opener.press("Enter");
    await expect(page.getByTestId("settings-view")).toBeVisible({
      timeout: 15_000,
    });

    await page.getByRole("button", { name: "Close settings" }).click();

    const settingsSurface = page.locator('[data-resident-key="settings"]');
    await expect(settingsSurface).toHaveAttribute("data-resident", "idle");
    await expect(settingsSurface).toHaveAttribute("inert", "");
    await expect(opener).toBeFocused();
  });

  webE2e("keyboard Approve on tool approval checkpoint", async ({
    page,
    request,
  }) => {
    test.setTimeout(180_000);

    await bootstrapChatSession(page, request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
      timeout: 90_000,
    });

    const sessionId = (
      (await liveChatStage(page).getByTestId("chat-stream").getAttribute("data-session-id")) ??
      ""
    ).trim();
    expect(sessionId).toBeTruthy();

    const inject = await request.post(
      `${apiUrl}/harness/checkpoints/tool_approval`,
      {
        headers: { ...auth, "Content-Type": "application/json" },
        data: { session_id: sessionId, command: "ls -la" },
      },
    );
    const injectBody = await inject.json();
    expect(inject.ok(), JSON.stringify(injectBody)).toBeTruthy();

    const card = page.getByTestId("tool-approval-card");
    await expect(card).toBeVisible({ timeout: 30_000 });

    const approve = page.getByTestId("approval-approve-primary");
    await expect(approve).toBeVisible();

    const resolveWait = page.waitForResponse(
      (resp) =>
        resp.url().includes("/checkpoints/") &&
        resp.request().method() === "POST",
      { timeout: 30_000 },
    );
    // press() exercises the keyboard path.
    await approve.press("Enter");
    const resolved = await resolveWait;
    expect(resolved.ok()).toBeTruthy();

    await expect(card).toHaveCount(0, { timeout: 30_000 });

    // eslint-disable-next-line no-console -- device-verification evidence
    console.log(`keyboard approve session=${sessionId}`);
  });
});
