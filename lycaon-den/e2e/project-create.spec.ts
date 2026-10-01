import { expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  E2E_FIXTURE_PROJECT,
  activateProject,
  apiConfig,
  apiCreateProjectWithRoot,
  e2eUniqueLabel,
  gotoShell,
  liveChatStage,
  trackCreatedProject,
  webE2e,
} from "./helpers.ts";
import type { Session } from "../src/api/types.ts";
import { STOCK_FRAME } from "../src/contributions/stock-frame.generated.ts";
import { CONTRIBUTION_FRAME_STORAGE_KEY } from "../src/contributions/contribution-frame-cache.ts";

/** The created project is reached through the chat it opened with. */
async function apiSessionProjectId(
  request: APIRequestContext,
  sessionId: string,
): Promise<string> {
  const { apiUrl, token } = apiConfig();
  const res = await request.get(`${apiUrl}/v1/sessions/${sessionId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.ok()).toBeTruthy();
  const session = (await res.json()) as Session;
  return session.project_id ?? "";
}

type FrameProbe = {
  /** Frames in which Home was the active surface with no veil over it. */
  homeFrames: number;
  /** Frames in which a veil covered the shell. */
  veilFrames: number;
};

declare global {
  interface Window {
    __projectCreateProbe?: FrameProbe & { stop: () => void };
  }
}

// An unveiled Home frame during project loading is a visible flash.
async function armFrameProbe(page: Page): Promise<void> {
  await page.evaluate(() => {
    const probe = { homeFrames: 0, veilFrames: 0, stop: () => undefined as void };
    let raf = 0;
    let armed = false;
    const tick = () => {
      if (armed) {
        const home = document.querySelector(
          '[data-resident-key="home"][data-resident="active"]',
        );
        const veil = document.querySelector(
          '[data-testid="shell-workspace-veil"]:not(.den-shell-workspace-veil--hidden)',
        );
        if (veil) probe.veilFrames += 1;
        if (home && !veil) probe.homeFrames += 1;
      }
      raf = requestAnimationFrame(tick);
    };
    // Capture runs before the button's own handler.
    document.addEventListener(
      "click",
      (event) => {
        const target = event.target as Element | null;
        if (target?.closest('[data-testid="project-launcher-new"]')) armed = true;
      },
      true,
    );
    raf = requestAnimationFrame(tick);
    probe.stop = () => cancelAnimationFrame(raf);
    window.__projectCreateProbe = probe;
  });
}

async function readFrameProbe(page: Page): Promise<FrameProbe> {
  return page.evaluate(() => {
    const probe = window.__projectCreateProbe;
    if (!probe) throw new Error("frame probe was not armed");
    probe.stop();
    return { homeFrames: probe.homeFrames, veilFrames: probe.veilFrames };
  });
}

webE2e.describe("project create", () => {
  webE2e("invalid theme cache permits project loading and creation without painting Home", async ({ page, request }) => {
    const errors: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    const frame = structuredClone(STOCK_FRAME);
    for (const theme of frame.themes) Reflect.deleteProperty(theme, "window_colors");
    await page.addInitScript(({ key, frame }) => {
      localStorage.setItem(key, JSON.stringify({ version: 1, frame }));
    }, { key: CONTRIBUTION_FRAME_STORAGE_KEY, frame });
    const origin = await apiCreateProjectWithRoot(
      request,
      E2E_FIXTURE_PROJECT,
      e2eUniqueLabel("Origin"),
    );

    await gotoShell(page);
    await activateProject(page, origin.id);
    await expect(page.getByTestId("shell-workspace-veil")).toHaveClass(
      /den-shell-workspace-veil--hidden/,
      { timeout: 60_000 },
    );

    await armFrameProbe(page);
    await page.getByTestId("project-switcher").click();
    await expect(page.getByTestId("project-launcher")).toBeVisible();
    await page.getByTestId("project-launcher-new").click();

    await expect(page.getByTestId("project-switcher")).toContainText("Untitled", {
      timeout: 60_000,
    });
    await expect(page.getByTestId("shell-workspace-veil")).toHaveClass(
      /den-shell-workspace-veil--hidden/,
      { timeout: 60_000 },
    );

    const stream = liveChatStage(page).getByTestId("chat-stream");
    await expect(stream).toHaveAttribute("data-session-id", /.+/, { timeout: 60_000 });
    const sessionId = (await stream.getAttribute("data-session-id")) ?? "";
    const createdId = await apiSessionProjectId(request, sessionId);
    expect(createdId).not.toBe("");
    expect(createdId).not.toBe(origin.id);
    trackCreatedProject(request, createdId);

    const probe = await readFrameProbe(page);
    expect(probe.veilFrames).toBeGreaterThan(0);
    expect(probe.homeFrames).toBe(0);
    expect(errors).toEqual([]);
  });
});
