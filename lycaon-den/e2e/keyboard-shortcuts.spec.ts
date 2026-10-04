import path from "node:path";
import { fileURLToPath } from "node:url";
import { mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import {
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import { appStateStorageSliceKey } from "../shared/app-state-storage.ts";

const screenshotDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../test-results/keyboard-shortcuts-screenshots",
);
mkdirSync(screenshotDir, { recursive: true });

const SEARCH_BINDING_ID = "painted-wolf/platform:key-search-open";
const LAUNCHER_BINDING_ID = "painted-wolf/platform:key-launcher-open";

async function openKeyboardSettings(page: Page) {
  await waitHarnessConnected(page);
  await page.keyboard.press("?");
  await expect(page.getByTestId("shortcut-help-overlay")).toBeVisible({
    timeout: 10_000,
  });
  await activateByKeyboard(page, "shortcut-help-open-settings");
  await expect(page.getByTestId("keyboard-settings-panel")).toBeVisible({
    timeout: 15_000,
  });
}

async function leaveSettings(page: Page) {
  await page.keyboard.press("Escape");
  const settingsSurface = page.locator('[data-resident-key="settings"]');
  await expect(settingsSurface).toHaveAttribute("data-resident", "idle");
  await expect(settingsSurface).toHaveAttribute("inert", "");
}

async function activateByKeyboard(page: Page, testId: string) {
  const control = page.getByTestId(testId);
  await control.focus();
  await control.press("Enter");
}

async function startRecording(page: Page, bindingId: string) {
  const testId = `keyboard-record-${bindingId}`;
  await activateByKeyboard(page, testId);
  await expect(page.getByTestId(testId)).toHaveAttribute("aria-pressed", "true");
}

async function waitForCaptureRelease(page: Page) {
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())),
  );
}

webE2e("den:harness — keyboard shortcuts rebind persist conflict help", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.goto("/");

  await expectShellReady(page);
  await page.getByTestId("shell-nav-dock").click();

  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByTestId("crossbar")).toBeVisible({
    timeout: 10_000,
  });
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("crossbar")).toHaveCount(0);

  await openKeyboardSettings(page);
  await expect(page.getByTestId(`keyboard-chord-${SEARCH_BINDING_ID}`)).toHaveAttribute(
    "data-binding",
    /^(⌘K|Ctrl\+K)$/,
  );
  await waitForCaptureRelease(page);

  await startRecording(page, SEARCH_BINDING_ID);
  await page.keyboard.press("ControlOrMeta+j");
  await waitForCaptureRelease(page);
  await expect(page.getByTestId(`keyboard-chord-${SEARCH_BINDING_ID}`)).toHaveAttribute(
    "data-binding",
    /^(⌘J|Ctrl\+J)$/,
  );


  await leaveSettings(page);

  await page.keyboard.press("ControlOrMeta+j");
  await expect(page.getByTestId("crossbar")).toBeVisible({
    timeout: 10_000,
  });
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("crossbar")).toHaveCount(0);

  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByTestId("crossbar")).toHaveCount(0);

  // Interactive collisions leave both commands runnable.
  await openKeyboardSettings(page);
  await startRecording(page, LAUNCHER_BINDING_ID);
  await page.keyboard.press("ControlOrMeta+j");
  await waitForCaptureRelease(page);
  await expect(page.getByTestId("keyboard-reserved-refuse")).toContainText(
    /already used by Open Crossbar/i,
  );
  await expect(page.getByTestId(`keyboard-chord-${LAUNCHER_BINDING_ID}`)).toHaveAttribute(
    "data-binding",
    /^(⌘P|Ctrl\+P)$/,
  );
  await expect(page.getByTestId(`keyboard-conflict-${SEARCH_BINDING_ID}`)).toHaveCount(0);
  await expect(page.getByTestId(`keyboard-conflict-${LAUNCHER_BINDING_ID}`)).toHaveCount(0);

  await page.reload();
  await expectShellReady(page);
  await openKeyboardSettings(page);
  await expect(page.getByTestId(`keyboard-chord-${SEARCH_BINDING_ID}`)).toHaveAttribute(
    "data-binding",
    /^(⌘J|Ctrl\+J)$/,
  );
  await leaveSettings(page);

  await page.keyboard.press("?");
  const help = page.getByTestId("shortcut-help-overlay");
  await expect(help).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId(`shortcut-help-chord-${SEARCH_BINDING_ID}`)).toHaveAttribute(
    "data-binding",
    /^(⌘J|Ctrl\+J)$/,
  );
  await page.keyboard.press("Escape");
  await expect(help).toBeHidden();

  // Loaded collisions discard custom overrides and restore the defaults.
  await page.evaluate(
    ({ key, launcherId }) => {
      const parsed = JSON.parse(localStorage.getItem(key) ?? "{}") as {
        overrides?: Record<string, string>;
      };
      parsed.overrides ??= {};
      parsed.overrides[launcherId] = "Mod+J";
      localStorage.setItem(key, JSON.stringify(parsed));
    },
    {
      key: appStateStorageSliceKey("shortcuts"),
      launcherId: LAUNCHER_BINDING_ID,
    },
  );
  await page.reload();
  await expectShellReady(page);
  await openKeyboardSettings(page);
  await expect(page.getByTestId(`keyboard-chord-${SEARCH_BINDING_ID}`)).toHaveAttribute("data-binding", /^(⌘K|Ctrl\+K)$/);
  await expect(page.getByTestId(`keyboard-chord-${LAUNCHER_BINDING_ID}`)).toHaveAttribute("data-binding", /^(⌘P|Ctrl\+P)$/);
  await expect(page.getByTestId(`keyboard-conflict-${SEARCH_BINDING_ID}`)).toHaveCount(0);
  await expect(page.getByTestId(`keyboard-conflict-${LAUNCHER_BINDING_ID}`)).toHaveCount(0);
});

webE2e("den:harness — leader key row in Keyboard Settings", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");
  await expectShellReady(page);
  await page.getByTestId("shell-nav-dock").click();

  await page.keyboard.press("?");
  await expect(page.getByTestId("shortcut-help-overlay")).toBeVisible({
    timeout: 10_000,
  });
  await activateByKeyboard(page, "shortcut-help-open-settings");
  await expect(page.getByTestId("keyboard-settings-panel")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("keyboard-row-nav.leader")).toBeVisible();
  await expect(page.getByTestId("keyboard-chord-nav.leader")).toHaveAttribute(
    "data-binding",
    /^(⌘;|Ctrl\+;)$/,
  );
  await leaveSettings(page);
});

webE2e("den:harness — chat focus shortcuts reveal an inline-stage chat", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await bootstrapChatSession(page);

  await page.getByTestId("project-files-entry").click();
  await expect(page.getByTestId("project-files-view")).toBeVisible();

  await page.keyboard.press("ControlOrMeta+;");
  await page.keyboard.press("c");
  const chat = liveChatStage(page).getByTestId("chat-view");
  await expect(chat).toBeVisible();
  await expect(chat).toHaveAttribute("aria-label", "Chat");
  // Focusing the chat lands in its composer.
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeFocused();

  await page.getByTestId("project-files-entry").click();
  await expect(page.getByTestId("project-files-view")).toBeVisible();

  await page.keyboard.press("ControlOrMeta+;");
  await page.keyboard.press("i");
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeFocused();
});
