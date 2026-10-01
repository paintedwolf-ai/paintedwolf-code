import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import { modelIndependentWebE2e as test, openProjectFilesFixture } from "./helpers.ts";
import { installLargeTreeFrames } from "./source-tree-frame-fixture.ts";

test.use({ trace: "retain-on-failure" });

async function sampleReveal(page: Page, action: () => Promise<void>) {
  const sampling = page.getByTestId("files-tree-scroll").evaluate(async element => {
    const samples: { rows: number; first: number; top: number; bottom: number }[] = [];
    const started = performance.now();
    while (performance.now() - started < 3000) {
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
      const bounds = element.getBoundingClientRect();
      const rows = [...element.querySelectorAll<HTMLElement>(".den-files-tree-virtual__row")].flatMap(row => {
        const rect = row.getBoundingClientRect();
        if (rect.bottom <= bounds.top || rect.top >= bounds.bottom) return [];
        const address = row.querySelector<HTMLElement>("[data-path]")?.dataset.path;
        const match = address?.match(/^folder-(\d+)(?:\/file-(\d+)\.ts)?$/);
        const rank = address === "." ? 0 : match ? 1 + Number(match[1]) * 10_000 +
          (match[2] === undefined ? 0 : Number(match[2]) + 1) : -1;
        return [{ rank, top: rect.top, bottom: rect.bottom }];
      }).sort((a, b) => a.top - b.top);
      samples.push({ rows: rows.length, first: rows[0]?.rank ?? -1,
        top: (rows[0]?.top ?? Infinity) - bounds.top, bottom: (rows.at(-1)?.bottom ?? -Infinity) - bounds.bottom });
    }
    return samples;
  });
  await action();
  return sampling;
}

test("file reveals paint continuous large-tree frames from the dropdown, tabs, and context menu", async ({ page, request }, info) => {
  info.setTimeout(90_000);
  await page.emulateMedia({ reducedMotion: "no-preference" });
  let delayed = false;
  await installLargeTreeFrames(page, async () => { if (delayed) await new Promise(resolve => setTimeout(resolve, 35)); });
  await openProjectFilesFixture(page, request, {
    prefix: "tree-reveal", name: "Tree reveal",
    seed(root) {
      for (const [folder, file] of [["folder-000", "file-0.ts"], ["folder-099", "file-9998.ts"]]) {
        mkdirSync(path.join(root, folder!), { recursive: true });
        writeFileSync(path.join(root, folder!, file!), `export const value = '${folder}';\n`);
      }
    },
  });
  const root = page.locator('.den-files-tree__label--dir[data-path="."]:visible').first();
  await root.click({ button: "right" });
  await page.getByTestId("files-ctx-expand-all").click();
  const first = page.locator('.den-files-tree__label--file[data-path="folder-000/file-0.ts"]');
  const last = page.locator('.den-files-tree__label--file[data-path="folder-099/file-9998.ts"]');
  await first.click();
  await expect(page.getByRole("tab", { name: "file-0.ts", exact: true })).toBeVisible();
  await root.press("End");
  await last.click();
  await expect(page.getByRole("tab", { name: "file-9998.ts", exact: true })).toBeVisible();
  delayed = true;

  await page.keyboard.press("ControlOrMeta+k");
  await page.getByTestId("crossbar-input").fill("Show open files");
  await page.getByTestId("crossbar-action-painted-wolf/platform:files-open-files-list").click();
  const dropdown = await sampleReveal(page, () => page.getByTestId("files-open-list").getByRole("button", { name: "file-0.ts", exact: true }).click());
  await expect(first).toBeVisible();
  const tab = await sampleReveal(page, () => page.getByRole("tab", { name: "file-9998.ts", exact: true }).click());
  await expect(last).toBeVisible();
  await page.getByRole("tab", { name: "file-0.ts", exact: true }).click({ button: "right" });
  const explicit = await sampleReveal(page, () => page.getByTestId("files-ctx-tab-reveal-tree").click());
  await expect(first).toBeFocused();

  writeFileSync(info.outputPath("tree-reveal-frames.json"), JSON.stringify({ dropdown, tab, explicit }, null, 2));
  for (const [name, samples, direction] of [["dropdown", dropdown, -1], ["tab", tab, 1], ["explicit", explicit, -1]] as const) {
    expect(samples.filter(sample => sample.rows === 0), `${name} must never paint an empty viewport`).toEqual([]);
    const positions = samples.map(sample => sample.first);
    expect(new Set(positions).size, `${name} must show intermediate tree locations`).toBeGreaterThan(4);
    expect(positions.every((position, index) => index === 0 || direction * (position - positions[index - 1]!) >= 0),
      `${name} must move towards the destination without jumping back`).toBe(true);
  }
  await page.screenshot({ path: info.outputPath("tree-reveal.png") });
});
