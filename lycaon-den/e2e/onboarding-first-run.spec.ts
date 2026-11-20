import { expect, type Page } from "@playwright/test";
import type { LocalGenerationStatus } from "../src/api/types.ts";
import { includedProvider, publishedTier } from "../src/test/local-generation-fixture.ts";
import { appStateStorageSliceKey } from "../shared/app-state-storage.ts";
import {
  apiClearDefaultModel,
  apiEnsureDefaultModel,
  apiJson,
  apiListProviders,
  seedAppState,
  seedFirstRunUnset,
  webE2e,
} from "./helpers.ts";

const ONBOARDING_STATE_KEY = appStateStorageSliceKey("onboarding");
const LAYOUT_STATE_KEY = appStateStorageSliceKey("layout");

async function serveCatalog(page: Page, published: boolean, runtime?: Partial<LocalGenerationStatus>) {
  const provider = includedProvider({
    state: "idle", recovery_action: "none",
    available_models: [{...publishedTier, download_available: published}],
    ...runtime,
  });
  await page.route("**/v1/providers", async route => {
    if (route.request().method() === "GET") await route.fulfill({json: {providers: [provider]}});
    else await route.continue();
  });
}

/** First-run model selection, starting layout, and workspace entry. */
webE2e.describe("onboarding first-run", () => {
  webE2e(
    "den:harness — local model → layout pick → welcome dismiss latches; reload skips gate",
    async ({ page, request }) => {
      await apiEnsureDefaultModel(request);
      await serveCatalog(page, true);
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
          completedSetupRevision?: number;
        };
        return onboarding.completedSetupRevision === 2;
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
      await serveCatalog(page, true);
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
          completedSetupRevision?: number;
        };
        return onboarding.completedSetupRevision === 2;
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
          completedSetupRevision?: number;
        };
        return onboarding.completedSetupRevision === 2;
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

  webE2e("an unpublished catalog defers model setup until downloads are offered", async ({page}) => {
    await seedFirstRunUnset(page);
    await serveCatalog(page, false);
    const mutations: string[] = [];
    page.on("request", request => {
      if (request.url().includes("/local-model") && request.method() !== "GET") mutations.push(request.method());
    });
    await page.goto("/");
    await expect(page.getByTestId("onboarding-layout")).toBeVisible({timeout: 60_000});
    await expect(page.getByTestId("onboarding-progress")).toHaveText("1 / 2");
    await page.getByTestId("onboarding-layout-continue").click();
    await expect(page.getByTestId("onboarding-welcome-settings-teach")).toBeVisible();
    await page.getByTestId("onboarding-welcome-dismiss").click();
    await expect(page.getByTestId("home-view")).toBeVisible({timeout: 60_000});
    await page.reload();
    await expect(page.getByTestId("home-view")).toBeVisible({timeout: 60_000});
    await expect(page.getByTestId("onboarding-gate")).toHaveCount(0);
    await serveCatalog(page, true);
    await page.reload();
    await expect(page.getByTestId("onboarding-local-model")).toBeVisible({timeout: 60_000});
    await expect(page.getByTestId("onboarding-progress")).toHaveText("1 / 1");
    expect(mutations).toEqual([]);
  });

  webE2e("a model imported before setup skips the published model choice", async ({page}) => {
    await serveCatalog(page, true, {
      state: "repair_required", recovery_action: "repair", selection_state: "selected", selected_model: publishedTier.id,
    });
    await seedFirstRunUnset(page);
    await page.goto("/");
    await expect(page.getByTestId("onboarding-layout")).toBeVisible({timeout: 60_000});
    await expect(page.getByTestId("onboarding-progress")).toHaveText("1 / 2");
    await expect(page.getByTestId("onboarding-layout-back")).toHaveCount(0);
    await page.getByTestId("onboarding-layout-continue").click();
    await page.getByTestId("onboarding-welcome-dismiss").click();
    await expect(page.getByTestId("home-view")).toBeVisible({timeout: 60_000});
  });

  webE2e("an existing installation sees only the model choice", async ({ page }) => {
    await serveCatalog(page, true);
    await seedAppState(page, {onboarding: {completedSetupRevision: 2}});
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
      await seedAppState(page);
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

webE2e("setup retries a canceled download and continues only after activation", async ({page}) => {
  const tier = {...publishedTier, download_bytes: 4 * 2 ** 30};
  const idle: LocalGenerationStatus = {state: "idle", recovery_action: "none", available_models: [tier]};
  let status = idle;
  let downloads = 0;
  await page.route("**/v1/providers", async route => {
    await route.fulfill({json: {providers: [includedProvider(status)]}});
  });
  await page.route("**/v1/providers/nativegen/local-model/select", async route => {
    expect(route.request().postDataJSON()).toEqual({model: tier.id});
    downloads++;
    status = {...idle, state: "downloading", target_model: tier.id, transfer_phase: "preparing"};
    await route.fulfill({json: status});
  });
  await page.route("**/v1/providers/nativegen/local-model/cancel", async route => {
    status = idle;
    await route.fulfill({json: status});
  });
  await page.route("**/v1/providers/nativegen/local-model", async route => {
    await route.fulfill({json: status});
  });
  await seedFirstRunUnset(page);
  await page.goto("/");
  const download = page.getByRole("button", {name: "Download 4 GiB and continue"});
  await expect(download).toBeVisible({timeout: 60_000});
  expect(downloads).toBe(0);
  await download.click();
  await expect(page.getByRole("status").filter({hasText:"Preparing Small…"})).toBeVisible();
  await page.getByRole("button", {name: "Cancel transfer"}).click();
  await expect(page.getByTestId("onboarding-layout")).toHaveCount(0);
  await page.getByRole("button", {name: "Retry download 4 GiB and continue"}).click();
  await expect(page.getByText("Preparing Small…")).toBeVisible();
  status = {...status, transfer_phase: "downloading", downloaded_bytes: 2 ** 30, total_bytes: tier.download_bytes};
  await expect(page.getByText("1 GiB of 4 GiB")).toBeVisible();
  await expect(page.getByText("25%", {exact:true})).toBeVisible();
  status = {...status, transfer_phase: "activating", downloaded_bytes: tier.download_bytes};
  await expect(page.getByText("Activating Small…")).toBeVisible();
  await expect(page.getByRole("button", {name: "Cancel transfer"})).toHaveCount(0);
  await expect(page.getByTestId("onboarding-layout")).toHaveCount(0);
  status = {...idle, selected_model: tier.id, state:"ready"};
  await expect(page.getByTestId("onboarding-layout")).toBeVisible();
  expect(downloads).toBe(2);
});

webE2e("failed model requests allow retry or workspace entry with external providers", async ({page}) => {
  const catalog = includedProvider({state: "idle", recovery_action: "none", available_models: [publishedTier]});
  let available = false;
  let starts = 0;
  let cancellations = 0;
  let removals = 0;
  const interrupted = {status: 503, json: {error: "Temporary host failure"}};
  await page.route("**/v1/providers", route => route.fulfill(available ? {json: {providers: [catalog]}} : interrupted));
  await page.route("**/v1/providers/nativegen/local-model/select", route => {
    starts++;
    return route.fulfill(starts === 1 ? interrupted : {json: {
      ...catalog.local_runtime, state: "downloading", transfer_phase: "downloading", target_model: publishedTier.id,
    }});
  });
  await page.route("**/v1/providers/nativegen/local-model", route => {
    if (route.request().method() === "DELETE") removals++;
    return route.fulfill(interrupted);
  });
  await page.route("**/v1/providers/nativegen/local-model/cancel", route => {
    cancellations++;
    return route.fulfill(interrupted);
  });
  await seedFirstRunUnset(page);
  await page.goto("/");
  const check = page.getByRole("button", {name: "Check again"});
  await expect(check).toBeVisible({timeout: 60_000});
  await check.click();
  await expect(page.getByRole("button", {name: "Continue without configuring a model"})).toBeEnabled();
  available = true;
  await check.click();
  await page.getByRole("button", {name: /^Download .* and continue$/}).click();
  await page.getByRole("button", {name: /^Retry download .* and continue$/}).click();
  await expect(page.getByTestId("onboarding-local-model").getByRole("alert")).toBeVisible();
  await page.getByRole("button", {name: "Cancel transfer"}).click();
  await expect.poll(() => cancellations).toBe(1);
  await page.getByRole("button", {name: "Finish model setup later"}).click();
  await page.getByTestId("onboarding-layout-continue").click();
  await page.getByTestId("onboarding-welcome-dismiss").click();
  await page.getByRole("button", {name: "Settings", exact: true}).click();
  await page.getByTestId("settings-nav-providers").click();
  await page.getByTestId("providers-add").click();
  await expect(page.getByTestId("providers-add-dialog")).toBeVisible();
  expect(starts).toBe(2);
  expect(removals).toBe(0);
  expect(cancellations).toBe(1);
});


webE2e("v1.0.2 users complete the current walkthrough once and keep their settings", async ({page, request}) => {
  await apiEnsureDefaultModel(request);
  const policyBefore = await apiJson(request, "GET", "/v1/settings/model-policy");
  const providerIds = (await apiListProviders(request)).map(p => p.id);
  let mutations = 0;
  await page.route("**/v1/providers/nativegen/local-model**", async route => {
    if (route.request().method() !== "GET") mutations++;
    await route.continue();
  });
  await serveCatalog(page, true);
  await seedAppState(page, {onboarding: {firstRunSetupCompleted: true}, layout: {startupCompanion: "files"}});
  await page.goto("/");
  await expect(page.getByTestId("onboarding-local-model")).toBeVisible({timeout: 60_000});
  expect(await page.evaluate(key => JSON.parse(localStorage.getItem(key)!), ONBOARDING_STATE_KEY)).toEqual({firstRunSetupCompleted:true});
  await page.getByRole("radio", {name: "Use no local model"}).check();
  await page.getByRole("button", {name:"Continue without a local model"}).click();
  await expect(page.getByTestId("onboarding-layout-split-radio")).toBeChecked();
  await page.getByTestId("onboarding-layout-continue").click();
  await page.getByTestId("onboarding-welcome-dismiss").click();
  await expect(page.getByTestId("home-view")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("home-view")).toBeVisible({timeout: 60_000});
  await expect(page.getByTestId("onboarding-gate")).toHaveCount(0);
  expect(mutations).toBe(1);
  expect(await apiJson(request, "GET", "/v1/settings/model-policy")).toEqual(policyBefore);
  expect((await apiListProviders(request)).map(p => p.id)).toEqual(providerIds);
});

webE2e("Escape defers a pre-1.1 model choice without persisting a host decision", async ({page}) => {
  await serveCatalog(page, true);
  await seedAppState(page, {onboarding: {firstRunSetupCompleted: true}});
  await page.goto("/");
  await expect(page.getByTestId("onboarding-local-model")).toBeVisible({timeout: 60_000});
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("home-view")).toBeVisible();
  expect(await page.evaluate(key => JSON.parse(localStorage.getItem(key)!), ONBOARDING_STATE_KEY)).toEqual({completedSetupRevision:2});
  await page.reload();
  await expect(page.getByTestId("onboarding-local-model")).toBeVisible({timeout: 60_000});
  await expect(page.getByTestId("onboarding-progress")).toHaveText("1 / 1");
});
