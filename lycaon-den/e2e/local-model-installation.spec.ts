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
  await expect(page.getByRole("status").filter({hasText:"Preparing Plain Bialy…"})).toBeVisible();
  await page.getByRole("button", {name: "Cancel transfer"}).click();
  await expect(page.getByTestId("onboarding-layout")).toHaveCount(0);
  await page.getByRole("button", {name: "Retry download 4 GiB and continue"}).click();
  await expect(page.getByText("Preparing Plain Bialy…")).toBeVisible();
  status = {...status, transfer_phase: "downloading", downloaded_bytes: 2 ** 30, total_bytes: tier.download_bytes};
  await expect(page.getByText("1 GiB of 4 GiB")).toBeVisible();
  await expect(page.getByText("25%", {exact:true})).toBeVisible();
  status = {...status, transfer_phase: "activating", downloaded_bytes: tier.download_bytes};
  await expect(page.getByText("Activating Plain Bialy…")).toBeVisible();
  await expect(page.getByRole("button", {name: "Cancel transfer"})).toHaveCount(0);
  await expect(page.getByTestId("onboarding-layout")).toHaveCount(0);
  status = {...idle, selected_model: tier.id, state:"ready"};
  await expect(page.getByTestId("onboarding-layout")).toBeVisible();
  expect(downloads).toBe(2);
});
