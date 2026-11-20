import { expect } from "@playwright/test";
import type { LocalGenerationStatus } from "../src/api/types.ts";
import { includedProvider, publishedTier } from "../src/test/local-generation-fixture.ts";
import { seedFirstRunUnset, webE2e } from "./helpers.ts";

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
