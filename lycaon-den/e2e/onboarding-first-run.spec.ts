import { expect } from "@playwright/test";
import { includedProvider } from "../src/test/local-generation-fixture.ts";
import { appStateStorageSliceKey } from "../shared/app-state-storage.ts";
import {
  apiClearDefaultModel,
  apiEnsureDefaultModel,
  seedAppState,
  seedFirstRunUnset,
  webE2e,
} from "./helpers.ts";

const ONBOARDING_STATE_KEY = appStateStorageSliceKey("onboarding");
const LAYOUT_STATE_KEY = appStateStorageSliceKey("layout");

/** First-run model selection, starting layout, and workspace entry. */
webE2e.describe("onboarding first-run", () => {
  webE2e(
    "den:harness — local model → layout pick → welcome dismiss latches; reload skips gate",
    async ({ page, request }) => {
      await apiEnsureDefaultModel(request);
      await seedFirstRunUnset(page);
      await page.goto("/");

      await expect(page.getByTestId("onboarding-gate")).toBeVisible({
        timeout: 60_000,
      });
      await page.getByRole("radio", { name: "Use no local model" }).check();
      await page.getByRole("button", { name: "Continue without a local model" }).click();
      await expect(page.getByTestId("onboarding-layout")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("onboarding-progress")).toHaveText("2 / 3");
      await expect(page.getByTestId("onboarding-layout-chat-radio")).toBeChecked();
      await page.getByTestId("onboarding-layout-split").click();
      await expect(page.getByTestId("onboarding-layout-split-radio")).toBeChecked();
      await expect.poll(() => page.evaluate((key) => {
        const raw = localStorage.getItem(key);
        if (!raw) return null;
        const layout = JSON.parse(raw) as { startupCompanion?: string };
        return layout.startupCompanion ?? null;
      }, LAYOUT_STATE_KEY)).toBe("files");

      await page.getByTestId("onboarding-layout-continue").click();
      await expect(page.getByTestId("onboarding-welcome")).toBeVisible({
        timeout: 15_000,
      });
      await expect(
        page.getByTestId("onboarding-welcome-settings-teach"),
      ).toBeVisible();
      await expect(page.getByTestId("onboarding-welcome")).toContainText(
        "AI providers live here",
      );
      await expect(page.getByTestId("onboarding-welcome")).toContainText(
        "Change them anytime",
      );

      await page.getByTestId("onboarding-welcome-dismiss").click();
      await expect(page.getByTestId("home-view")).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.getByTestId("onboarding-gate")).toHaveCount(0);

      const latch = await page.evaluate((key) => {
        const raw = localStorage.getItem(key);
        if (!raw) return null;
        const onboarding = JSON.parse(raw) as {
          firstRunSetupCompleted?: boolean;
        };
        return onboarding.firstRunSetupCompleted === true;
      }, ONBOARDING_STATE_KEY);
      expect(latch).toBe(true);

      // Reload reads the persisted completion state.
      await page.reload();
      await expect(page.getByTestId("home-view")).toBeVisible({
        timeout: 60_000,
      });
      await expect(page.getByTestId("onboarding-gate")).toHaveCount(0);

    },
  );

  webE2e(
    "den:harness — Escape steps layout → welcome; Escape on welcome latches",
    async ({ page, request }) => {
      await apiEnsureDefaultModel(request);
      await seedFirstRunUnset(page);
      await page.goto("/");
      await page.getByRole("radio", { name: "Use no local model" }).check();
      await page.getByRole("button", { name: "Continue without a local model" }).click();
      await expect(page.getByTestId("onboarding-layout")).toBeVisible({
        timeout: 15_000,
      });
      await page.keyboard.press("Escape");
      await expect(page.getByTestId("onboarding-welcome")).toBeVisible({
        timeout: 15_000,
      });
      const latchAfterSkip = await page.evaluate((key) => {
        const raw = localStorage.getItem(key);
        if (!raw) return false;
        const onboarding = JSON.parse(raw) as {
          firstRunSetupCompleted?: boolean;
        };
        return onboarding.firstRunSetupCompleted === true;
      }, ONBOARDING_STATE_KEY);
      expect(latchAfterSkip).toBe(false);

      await page.keyboard.press("Escape");
      await expect(page.getByTestId("home-view")).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.getByTestId("onboarding-gate")).toHaveCount(0);
      const latch = await page.evaluate((key) => {
        const raw = localStorage.getItem(key);
        if (!raw) return false;
        const onboarding = JSON.parse(raw) as {
          firstRunSetupCompleted?: boolean;
        };
        return onboarding.firstRunSetupCompleted === true;
      }, ONBOARDING_STATE_KEY);
      expect(latch).toBe(true);
    },
  );

  webE2e("offline installation with a damaged bundle starts at layout", async ({ page }) => {
    const provider = includedProvider({state: "repair_required", recovery_action: "repair", offline_installer: true, seed_available: false});
    await page.route("**/v1/providers", async route => {
      if (route.request().method() === "GET") await route.fulfill({json: {providers: [provider]}});
      else await route.continue();
    });
    await seedFirstRunUnset(page);
    await page.goto("/");
    await expect(page.getByTestId("onboarding-layout")).toBeVisible({timeout: 60_000});
    await expect(page.getByTestId("onboarding-progress")).toHaveText("1 / 2");
    await expect(page.getByTestId("onboarding-layout-back")).toHaveCount(0);
    await page.getByTestId("onboarding-layout-continue").click();
    await expect(page.getByTestId("onboarding-progress")).toHaveText("2 / 2");
    await page.getByTestId("onboarding-welcome-dismiss").click();
    await expect(page.getByTestId("home-view")).toBeVisible();
  });

  webE2e("an existing installation sees only the model choice", async ({ page }) => {
    await seedAppState(page, {onboarding: {firstRunSetupCompleted: true, localModelOnboardingCompleted: false}});
    await page.goto("/");
    await expect(page.getByTestId("onboarding-local-model")).toBeVisible({timeout: 60_000});
    await expect(page.getByTestId("onboarding-progress")).toHaveText("1 / 1");
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("onboarding-gate")).toHaveCount(0);
  });

  webE2e(
    "den:harness — latch + cleared config → provider card only (W8/W12)",
    async ({ page, request }) => {
      await apiEnsureDefaultModel(request);
      await seedAppState(page, {
        onboarding: { firstRunSetupCompleted: true },
      });
      await apiClearDefaultModel(request);
      await page.goto("/");

      await expect(page.getByTestId("home-view")).toBeVisible({
        timeout: 60_000,
      });
      await expect(page.getByTestId("onboarding-gate")).toHaveCount(0);
      // Home and chat share the missing-provider card.
      await expect(page.getByTestId("no-provider-banner").first()).toBeVisible({
        timeout: 30_000,
      });
      await page.getByTestId("no-provider-open-settings").first().click();
      await expect(page.getByTestId("settings-view")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("providers-settings")).toBeVisible({
        timeout: 15_000,
      });
      const addProvider = page.getByTestId("providers-add");
      await addProvider.click();
      await expect(page.getByTestId("providers-add-dialog")).toBeVisible();
      await expect(page.getByTestId("settings-view")).toBeVisible();
      await expect(page.getByTestId("add-provider-filter")).toBeFocused();
      await page.getByTestId("add-provider-kind-ollama").click();
      await expect(page.getByTestId("add-provider-label")).toBeFocused();
      await page.getByTestId("add-provider-cancel").click();
      await expect(page.getByTestId("providers-add-dialog")).toHaveCount(0);
      await expect(page.getByTestId("settings-view")).toBeVisible();
      await expect(addProvider).toBeFocused();

    },
  );
});
