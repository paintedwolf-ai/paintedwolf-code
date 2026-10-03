import { readFileSync } from "node:fs";
import { expect, type Page } from "@playwright/test";
import { appStateStorageSliceKey } from "../shared/app-state-storage.ts";
import {
  bootstrapChatSession,
  seedAppState,
  webE2e,
} from "./helpers.ts";

const WHATS_NEW_STATE_KEY = appStateStorageSliceKey("whatsNew");

const ISSUES_URL = "https://github.com/paintedwolf-ai/paintedwolf-code/issues";

// What's New shows only a version whose notes the changelog carries.
const RELEASED_VERSION = /^## \[([^\]]+)\]/m.exec(
  readFileSync(new URL("../../CHANGELOG.md", import.meta.url), "utf8"),
)?.[1] ?? "";

async function openAboutPanel(page: Page) {
  await page
    .getByTestId("shell-nav-dock")
    .getByRole("button", { name: "Settings" })
    .click();
  await expect(page.getByTestId("settings-view")).toBeVisible({
    timeout: 15_000,
  });
  await page.getByTestId("settings-nav-general").click();
  await page.getByTestId("general-tab-about").click();
  await expect(page.getByTestId("general-panel-about")).toBeVisible();
  await expect(page.getByTestId("about-settings-panel")).toBeVisible();
}

async function whatsNewLatch(page: Page): Promise<string | null> {
  return page.evaluate((key) => {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const whatsNew = JSON.parse(raw) as { lastSeenVersion?: string };
    return whatsNew.lastSeenVersion ?? null;
  }, WHATS_NEW_STATE_KEY);
}

webE2e.describe("feedback + what's new den", () => {
  webE2e(
    "Report a bug saves the bundle and opens GitHub Issues only after save",
    async ({ page }) => {
      const externalPosts: string[] = [];
      await page.route("**/*", async (route) => {
        const req = route.request();
        const url = req.url();
        if (req.method() === "POST") {
          const local =
            url.includes("127.0.0.1") ||
            url.includes("localhost") ||
            url.startsWith(page.url().split("/").slice(0, 3).join("/"));
          if (!local) externalPosts.push(url);
        }
        await route.continue();
      });

      const opened: string[] = [];
      await page.exposeFunction("__e2eNoteOpen", (url: string) => {
        opened.push(url);
      });
      await page.addInitScript(() => {
        const w = window as unknown as {
          open: typeof window.open;
          __e2eNoteOpen?: (url: string) => void;
        };
        const nativeOpen = w.open.bind(window);
        w.open = ((url?: string | URL) => {
          if (url != null) {
            w.__e2eNoteOpen?.(String(url));
          }
          return nativeOpen(url, "_blank", "noopener,noreferrer");
        }) as typeof window.open;
      });

      await bootstrapChatSession(page);

      await openAboutPanel(page);
      await expect(page.getByTestId("about-report-bug")).toBeVisible();
      await page.getByTestId("about-report-bug").click();
      await expect(page.getByTestId("report-bug-dialog")).toBeVisible();
      await expect(page.getByTestId("report-bug-explain")).toContainText(
        /nothing is sent automatically/i,
      );

      await expect(page.getByTestId("report-bug-open-issues")).toHaveCount(0);

      await page.getByTestId("report-bug-save").click();
      await expect(page.getByTestId("report-bug-path")).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.getByTestId("report-bug-filename")).toContainText(
        /Do not attach/i,
      );
      await expect(page.getByTestId("report-bug-open-issues")).toContainText(
        /GitHub Issues/i,
      );

      // The app's own Issues page opens without a confirmation step.
      await page.getByTestId("report-bug-open-issues").click();
      await expect
        .poll(() => opened.some((u) => u.startsWith(ISSUES_URL)))
        .toBe(true);
      expect(externalPosts).toEqual([]);
    },
  );

  webE2e(
    "What's New shows for an older latch and Got it latches",
    async ({ page }) => {
      // A released version supplies visible release notes.
      await page.route("**/health", async (route) => {
        const res = await route.fetch();
        const json = (await res.json()) as Record<string, unknown>;
        await route.fulfill({
          status: res.status(),
          contentType: "application/json",
          body: JSON.stringify({ ...json, version: RELEASED_VERSION }),
        });
      });

      await seedAppState(page, {
        onboarding: { firstRunSetupCompleted: true },
        whatsNew: { lastSeenVersion: "0.9.0" },
      });
      await page.goto("/");
      await expect(page.getByTestId("home-view")).toBeVisible({
        timeout: 30_000,
      });
      expect(await whatsNewLatch(page)).toBe("0.9.0");

      await expect(page.getByTestId("whats-new-card")).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.getByTestId("whats-new-nudge")).toContainText(
        `What’s new in ${RELEASED_VERSION}`,
      );
      await expect(page.getByTestId("whats-new-got-it")).toBeVisible();
      await page.getByTestId("whats-new-got-it").click();
      await expect(page.getByTestId("whats-new-card")).toHaveCount(0);

      expect(await whatsNewLatch(page)).toBe(RELEASED_VERSION);
    },
  );

  webE2e(
    "unset What's New latch shows nothing on first run seed",
    async ({ page }) => {
      await seedAppState(page, {
        onboarding: { firstRunSetupCompleted: true },
        // whatsNew omitted → unset
      });
      await page.goto("/");
      await expect(page.getByTestId("home-view")).toBeVisible({
        timeout: 30_000,
      });
      // The first observed version seeds without a card.
      await expect
        .poll(async () => page.getByTestId("whats-new-card").count(), {
          timeout: 10_000,
        })
        .toBe(0);
    },
  );
});
