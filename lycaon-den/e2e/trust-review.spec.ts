import { mkdirSync, unlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiConfig, openProjectFilesFixture, webE2e } from "./helpers.ts";

webE2e("trust review opens captured diffs, clears the chip, and keeps later removals unread", async ({ page, request }, testInfo) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "trust-review", name: "Trust review",
    seed: root => writeFileSync(path.join(root, "AGENTS.md"), "Use the existing tests.\n"),
  });
  const chip = page.getByTestId("status-chip-trust");
  await expect(chip).toHaveAttribute("data-state", "unseen");
  await chip.click();
  await page.getByTestId("trust-open-review").click();
  const review = page.getByTestId("trust-review-page").filter({ visible: true });
  await expect(review).toBeVisible();
  await expect(chip).toHaveAttribute("data-state", "seen");
  await expect(review.getByTestId("trust-review-file")).toContainText("AGENTS.md");
  await page.screenshot({ path: testInfo.outputPath("trust-group.png") });
  await expect(review.locator("select, .den-files-toolbar")).toHaveCount(0);
  await chip.click();
  const popup = page.getByTestId("status-popover-trust");
  await expect(popup.getByTestId("trust-popover-title")).toHaveText("1 file changed");
  await expect(review.getByTestId("trust-review-count")).toHaveText("1 file changed");
  await page.getByTestId("trust-open-review").click();
  await expect(review.getByTestId("trust-review-file")).toHaveCount(1);
  await expect(page.locator('[data-testid="files-tab"]').filter({ hasText: "Trust changes" })).toHaveCount(1);
  await expect(page.getByText("Show all changes", { exact: false })).toHaveCount(0);

  writeFileSync(path.join(root, "AGENTS.md"), "Add focused tests for changes.\n");
  await expect(chip).toHaveAttribute("data-state", "unseen", { timeout: 20_000 });
  await chip.click();
  await page.getByTestId("trust-popover-row-agents_md").click();
  await expect(review).toBeVisible();
  const groupKey = await page.locator('[data-testid="files-tab"][aria-selected="true"]').evaluate(element => element.closest("[data-key]")?.getAttribute("data-key"));
  await review.getByTestId("trust-review-file").click();
  await expect(page.getByTestId("git-review-preview")).toHaveCount(0);
  const version = page.getByTestId("file-version-document").filter({ visible: true });
  await expect(version).toContainText("Add focused tests for changes.");
  await page.screenshot({ path: testInfo.outputPath("trust-diff.png") });
  await expect(chip).toHaveAttribute("data-state", "seen");
  await page.locator(`[data-files-ctx="tab"][data-key="${groupKey}"]`).getByRole("tab").click();
  await expect(review.getByTestId("trust-review-file")).toContainText("Modified");

  unlinkSync(path.join(root, "AGENTS.md"));
  await expect(chip).toHaveAttribute("data-state", "unseen", { timeout: 20_000 });
  await chip.click();
  await page.getByTestId("trust-open-review").click();
  await expect(review.getByTestId("trust-review-file")).toContainText("Deleted");
  await expect(chip).toHaveAttribute("data-state", "seen");
  await chip.click();
  await expect(popup.getByTestId("trust-popover-title")).toHaveText("1 file changed");
  await page.getByTestId("trust-popover-row-agents_md").click();
  await expect(review.getByTestId("trust-review-file")).toContainText("Deleted");
  await expect(page.locator('[data-testid="files-tab"]').filter({ hasText: "Trust changes" })).toHaveCount(1);
  await review.getByTestId("trust-review-file").click();
  await expect(version).toContainText("Add focused tests for changes.");
  await page.screenshot({ path: testInfo.outputPath("trust-removal.png") });
  const { apiUrl, token } = apiConfig();
  const response = await request.get(`${apiUrl}/v1/projects/${project.id}/trust`, { headers: { Authorization: `Bearer ${token}` } });
  expect((await response.json()).unread_count).toBe(0);
});

webE2e("trust popup stays compact without scrolling at narrow sizes and enlarged text", async ({ page, request }, testInfo) => {
  await openProjectFilesFixture(page, request, {
    prefix: "trust-popup", name: "Trust popup layout",
    seed: root => {
      const overlay = path.join(root, ".paintedwolf");
      mkdirSync(path.join(overlay, "skills", "example"), { recursive: true });
      mkdirSync(path.join(overlay, "prompt_files"), { recursive: true });
      writeFileSync(path.join(root, "AGENTS.md"), "Project instructions.\n");
      writeFileSync(path.join(overlay, "skills/example/SKILL.md"), "---\nname: example\ndescription: Example\n---\nInstructions.\n");
      writeFileSync(path.join(overlay, "approvals.yaml"), "posture: strict\n");
      writeFileSync(path.join(overlay, "mcp.yaml"), "providers:\n  - id: example\n    enabled: true\n");
      writeFileSync(path.join(overlay, "scanners.yaml"), "scanners: []\n");
      writeFileSync(path.join(overlay, "prompt_files/brief.txt"), "Be brief.\n");
      writeFileSync(path.join(overlay, "extensions.yaml"), "format: 1\nsuggest:\n  - id: example/pack\ndisabled:\n  - example/unit\n");
    },
  });
  await page.route("**/v1/projects/*/trust/review", route => route.fulfill({
    status: 503, contentType: "application/json",
    body: JSON.stringify({ code: "project_inventory_unavailable", message: `Cannot read ${"nested-folder/".repeat(100)}AGENTS.md` }),
  }));
  for (const size of [{ width: 1100, height: 760, fontSize: 16 }, { width: 1440, height: 760, fontSize: 20 }, { width: 1440, height: 460, fontSize: 20 }]) {
    await page.setViewportSize({ width: size.width, height: size.height });
    await page.evaluate(fontSize => { document.documentElement.style.fontSize = `${fontSize}px`; }, size.fontSize);
    await page.getByTestId("status-chip-trust").click();
    const popup = page.getByTestId("status-popover-trust");
    await expect(popup).toBeVisible();
    await expect.poll(async () => popup.evaluate(element => ({
      x: element.scrollWidth <= element.clientWidth + 1,
      y: element.scrollHeight <= element.clientHeight + 1,
      bounded: element.getBoundingClientRect().right <= innerWidth && element.getBoundingClientRect().bottom <= innerHeight,
    }))).toEqual({ x: true, y: true, bounded: true });
    await expect(page.getByTestId("trust-open-review")).toBeVisible();
    await page.getByTestId("trust-open-review").click();
    // The popup shows the host's reason, however long, without scrolling.
    await expect(popup.getByRole("alert")).toContainText("Cannot read nested-folder/");
    await expect.poll(() => popup.evaluate(element => element.scrollHeight <= element.clientHeight + 1)).toBe(true);
    await expect(page.getByTestId("status-chip-trust")).toHaveAttribute("data-state", "unseen");
    await page.screenshot({ path: testInfo.outputPath(`trust-popup-${size.width}-${size.height}.png`) });
    expect(await page.getByTestId("status-chip-trust").evaluate(element => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
    await page.keyboard.press("Escape");
  }
});
