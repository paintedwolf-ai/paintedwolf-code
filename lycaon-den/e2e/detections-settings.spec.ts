import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  e2eTempDir,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";
import type { DetectionPackImportResult, DetectionPackList } from "../src/api/types.ts";

async function openDetectionsTab(page: Page) {
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByTestId("settings-nav-approvals").click();
  await page.getByTestId("approvals-tab-detections").click();
  await expect(page.getByTestId("detections-panel")).toBeVisible({
    timeout: 15_000,
  });
}

async function packIdsOnPage(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>("[data-testid^='detection-pack-'][data-source]")].map(
      (el) => el.getAttribute("data-testid") ?? "",
    ),
  );
}

webE2e("den:harness — detections catalog, scope, toggle, and import lifecycle", async ({
  page,
  request,
}) => {
  test.setTimeout(180_000);

  const { apiUrl, token } = apiConfig();
  const auth = { Authorization: `Bearer ${token}` };

  const listRes = await request.get(`${apiUrl}/v1/detection-packs`, {
    headers: auth,
  });
  expect(listRes.ok(), await listRes.text()).toBeTruthy();
  const listed = (await listRes.json()) as DetectionPackList;
  expect(listed.packs.length).toBeGreaterThanOrEqual(10);
  expect(listed.packs.map((p) => p.id)).toEqual(
    expect.arrayContaining([
      "aws-cli",
      "terraform-cli",
      "egress-providers",
      "gcp-structured-actions",
      "azure-structured-actions",
    ]),
  );
  expect(
    listed.packs.find((p) => p.id === "gcp-structured-actions")?.source,
  ).toBe("generated");
  expect(
    listed.packs.find((p) => p.id === "azure-structured-actions")?.source,
  ).toBe("generated");
  expect(
    listed.packs.find((p) => p.id === "terraform-cli")?.equivalent_binaries,
  ).toEqual(expect.arrayContaining(["tofu", "terragrunt", "tf"]));

  // Persist toggle via API before opening UI.
  const off = await request.patch(`${apiUrl}/v1/detection-packs/aws-cli`, {
    headers: { ...auth, "Content-Type": "application/json" },
    data: { enabled: false },
  });
  expect(off.ok(), await off.text()).toBeTruthy();

  await page.goto("/");
  await waitHarnessConnected(page);

  const project = await apiFindHarnessProject(request);
  await activateProject(page, project.id);

  const detectionsResponse = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === "/v1/detection-packs" &&
      r.request().method() === "GET",
    { timeout: 30_000 },
  );
  await openDetectionsTab(page);
  const detRes = await detectionsResponse;
  const detBody = await detRes.text();
  expect(
    detRes.ok(),
    `browser GET detections ${detRes.status()}: ${detBody.slice(0, 500)}`,
  ).toBeTruthy();
  const browserList = JSON.parse(detBody) as DetectionPackList;
  expect(
    browserList.packs.find((p) => p.id === "terraform-cli")?.equivalent_binaries,
  ).toEqual(expect.arrayContaining(["tofu", "terragrunt"]));
  expect(
    browserList.packs.find((p) => p.id === "gcp-structured-actions")?.source,
  ).toBe("generated");
  expect(
    browserList.packs.find((p) => p.id === "azure-structured-actions")?.source,
  ).toBe("generated");

  const loadError = page.getByTestId("detection-load-error");
  if (await loadError.count()) {
    throw new Error(
      `detections load failed: ${await loadError.textContent()} panel=${await page.getByTestId("detections-panel").textContent()}`,
    );
  }
  await expect(page.getByTestId("detection-empty")).toHaveCount(0);

  await expect
    .poll(() => packIdsOnPage(page), { timeout: 30_000 })
    .toEqual(
      expect.arrayContaining([
        "detection-pack-aws-cli",
        "detection-pack-terraform-cli",
        "detection-pack-egress-providers",
        "detection-pack-gcp-structured-actions",
        "detection-pack-azure-structured-actions",
      ]),
    );

  const ids = await packIdsOnPage(page);
  expect(ids.length).toBeGreaterThanOrEqual(10);

  await expect(page.getByTestId("detection-pack-aws-cli")).toHaveAttribute(
    "data-enabled",
    "false",
  );
  // Vitest covers badge and incompleteness copy; soft-provider recovery can take
  // the settings stage during a longer check.
  await expect(page.getByTestId("detection-egress-note")).toBeVisible({
    timeout: 5_000,
  });

  // Re-enable via API + confirm UI after reopen.
  const on = await request.patch(`${apiUrl}/v1/detection-packs/aws-cli`, {
    headers: { ...auth, "Content-Type": "application/json" },
    data: { enabled: true },
  });
  expect(on.ok(), await on.text()).toBeTruthy();
  await openDetectionsTab(page);
  await expect(page.getByTestId("detection-pack-aws-cli")).toHaveAttribute(
    "data-enabled",
    "true",
    { timeout: 30_000 },
  );

  // Project Configuration omits Detections.
  await page.evaluate(async () => {
    const h = (
      window as unknown as {
        __harness: { click(testid: string): Promise<{ ok: boolean }> };
      }
    ).__harness;
    await h.click("project-configuration-entry");
  });
  await expect(page.getByTestId("project-configuration-header")).toBeVisible({
    timeout: 15_000,
  });
  await page.evaluate(async () => {
    const h = (
      window as unknown as {
        __harness: { click(testid: string): Promise<{ ok: boolean }> };
      }
    ).__harness;
    const clicked = await h.click("project-context-entry-approvals");
    if (!clicked.ok) throw new Error("project-context-entry-approvals click failed");
  });
  await expect(page.locator('[data-testid="approvals-settings"]:visible')).toHaveAttribute(
    "data-scope",
    "project",
    { timeout: 15_000 },
  );
  await expect(page.locator('[data-testid="approvals-tab-detections"]:visible')).toHaveCount(0);

  // Import dry-run + commit via API; remove via API (picker is native).
  const scratch = e2eTempDir(request, "detection-pack");
  fs.mkdirSync(path.join(scratch, "rules"));
  fs.writeFileSync(
    path.join(scratch, "pack.yaml"),
    "id: harness-scratch\nlabel: Harness scratch\ndescription: e2e pack\n",
  );
  fs.writeFileSync(
    path.join(scratch, "rules", "one.yml"),
    `title: Scratch delete
id: 55555555-5555-4555-8555-555555555555
description: d
level: high
logsource: {product: lycaon, service: tool_exec}
detection:
  sel: {Image: harness-scratch-bin}
  condition: sel
`,
  );
  fs.writeFileSync(path.join(scratch, "README.md"), "skip me");

  const dry = await request.post(`${apiUrl}/v1/detection-packs`, {
    headers: { ...auth, "Content-Type": "application/json" },
    data: { path: scratch, dry_run: true },
  });
  expect(dry.ok(), await dry.text()).toBeTruthy();
  const dryBody = (await dry.json()) as DetectionPackImportResult;
  expect(dryBody.ignored).toEqual(expect.arrayContaining(["README.md"]));

  const commit = await request.post(
    `${apiUrl}/v1/detection-packs`,
    {
      headers: { ...auth, "Content-Type": "application/json" },
      data: { path: scratch, dry_run: false },
    },
  );
  expect(commit.ok(), await commit.text()).toBeTruthy();

  await openDetectionsTab(page);
  await expect
    .poll(() => packIdsOnPage(page), { timeout: 30_000 })
    .toEqual(expect.arrayContaining(["detection-pack-harness-scratch"]));
  await expect(page.getByTestId("detection-pack-aws-cli-remove")).toHaveCount(0);
  await expect(
    page.getByTestId("detection-pack-harness-scratch-remove"),
  ).toBeVisible();

  const del = await request.delete(
    `${apiUrl}/v1/detection-packs/harness-scratch`,
    { headers: auth },
  );
  expect(del.status()).toBe(204);

});
