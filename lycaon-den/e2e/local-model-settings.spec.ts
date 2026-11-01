import { expect } from "@playwright/test";
import { includedProvider } from "../src/test/local-generation-fixture.ts";
import { bootstrapActiveProject, webE2e } from "./helpers.ts";

webE2e("included model recovery uses host readiness and preserves remote providers", async ({ page, request }, testInfo) => {
  let provider = includedProvider({ state: "repair_required", recovery_action: "repair", detail: "Fixture payload is missing" });
  let repairRequests = 0;
  await page.route("**/v1/providers", async route => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, providers: [provider, ...body.providers.filter((item: { id: string }) => item.id !== provider.id)] } });
  });
  await page.route("**/v1/providers/nativegen/repair", async route => {
    expect(route.request().method()).toBe("POST");
    repairRequests++;
    provider = includedProvider();
    await route.fulfill({ json: { ok: true, status: provider.local_runtime } });
  });
  await bootstrapActiveProject(page, request);
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByTestId("settings-nav-providers").click();
  await page.getByTestId("providers-row-nativegen").click();
  const card = page.getByTestId("provider-card-nativegen");
  await expect(card).toContainText("Repair needed");
  await expect(card.getByTestId("provider-secret-trust-nativegen")).toBeVisible();
  await expect(card.getByTestId("provider-remove-nativegen")).toHaveCount(0);
  await expect(card.getByTestId("provider-endpoint-nativegen")).toHaveCount(0);
  await card.getByRole("button", { name: "Repair model" }).click();
  await expect(card.getByTestId("local-model-context")).toContainText("16,384");
  await expect(card.getByRole("status")).toHaveText("Ready");
  expect(repairRequests).toBe(1);
  await page.screenshot({ path: testInfo.outputPath("included-model-ready.png") });
  await page.getByRole("main").getByRole("button", { name: "AI providers", exact: true }).click();
  await expect(page.getByTestId("providers-add")).toBeVisible();
});
