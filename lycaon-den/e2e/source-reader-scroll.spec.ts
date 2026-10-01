import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page, type APIRequestContext } from "@playwright/test";
import type { TextComparisonSource, ProjectSourceReadResponse, PutProjectSourceRequest } from "../src/api/types.ts";
import { apiConfig, modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

async function saveRetainedFixture(request: APIRequestContext, source: string,
  options: { headers: { Authorization: string }; params: { root_id: string; path: string } }, content: string) {
  const response = await request.get(source, options);
  expect(response.ok(), await response.text()).toBe(true);
  const before = await response.json() as ProjectSourceReadResponse;
  if (!before.sha256) throw new Error("Retained fixture source has no content SHA.");
  const saved = await request.put(source, { headers: options.headers, data: {
    ...options.params, operation_id: crypto.randomUUID(), content,
    encoding: "utf-8", base_sha256: before.sha256,
  } satisfies PutProjectSourceRequest });
  expect(saved.ok(), await saved.text()).toBe(true);
}

modelIndependentWebE2e("historical CodeMirror reserves unloaded ranges and reaches the actual end", async ({ page, request }, testInfo) => {
  const text = (version: string) => Array.from({ length: 3000 }, (_, i) => `export const ${version}${i} = ${i};\n`).join("");
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "historical-scroll", name: "Historical scroll",
    seed: root => writeFileSync(path.join(root, "history.ts"), text("before")),
  });
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const source = `${apiUrl}/v1/projects/${project.id}/source`;
  const params = { path: "history.ts", root_id: project.roots[0]!.id };
  await saveRetainedFixture(request, source, { headers, params }, text("after"));
  await page.getByTestId("files-tree-file").filter({ hasText: "history.ts" }).click();
  await expect(page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content")).toBeEditable();
  const offsets: number[] = [];
  page.on("request", request => {
    const url = new URL(request.url());
    if (/\/source\/views\/[^/]+\/presentations\/[^/]+\/rows$/.test(url.pathname)) offsets.push(Number(url.searchParams.get("offset") ?? 0));
  });
  await page.getByTestId("file-version-trigger").click();
  const historyMenu = page.getByRole("menu", { name: "Version history" });
  await expect.poll(() => historyMenu.getByRole("menuitemradio").count()).toBeGreaterThan(1);
  await historyMenu.getByRole("menuitemradio").last().click();
  const reader = page.getByTestId("source-reader").filter({ visible: true });
  await expect(reader.locator(".cm-content")).toHaveAttribute("contenteditable", "false");
  await expect(reader.locator(".cm-content")).toContainText("after0");
  const scroller = reader.locator(".cm-scroller");
  await expect.poll(() => scroller.evaluate(element => element.scrollHeight)).toBeGreaterThan(80_000);
  const initialHeight = await scroller.evaluate(element => element.scrollHeight);
  const initialRequests = offsets.length;
  await page.screenshot({ path: testInfo.outputPath("historical-start.png") });
  await scroller.evaluate(element => { element.scrollTop = element.scrollHeight; });
  await expect(reader.locator(".cm-content")).toContainText("before2999", { timeout: 15_000 });
  await expect.poll(() => scroller.evaluate(element => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThan(50);
  expect(Math.abs(await scroller.evaluate(element => element.scrollHeight) - initialHeight)).toBeLessThan(100);
  expect(offsets.slice(initialRequests).some(offset => offset > 5000)).toBe(true);
  expect(offsets.length - initialRequests).toBeLessThan(10);
  expect(await reader.locator(".cm-line").count()).toBeLessThan(200);
  await scroller.hover();
  await page.mouse.wheel(0, 2000);
  expect(Math.abs(await scroller.evaluate(element => element.scrollHeight) - initialHeight)).toBeLessThan(100);
  await page.screenshot({ path: testInfo.outputPath("historical-end.png") });
  for (const line of [1501, 51, 2801]) {
    await page.getByRole("button", { name: "Go to line", exact: true }).click();
    await page.getByRole("textbox", { name: "Go to line", exact: true }).fill(String(line));
    await page.getByRole("button", { name: "Go", exact: true }).click();
    await expect.poll(() => reader.evaluate((element, text) => {
      const scroller = element.querySelector(".cm-scroller")!;
      const bounds = scroller.getBoundingClientRect();
      return [...scroller.querySelectorAll(".cm-line")].some(row => row.textContent?.includes(text)
        && row.getBoundingClientRect().top >= bounds.top && row.getBoundingClientRect().bottom <= bounds.bottom);
    }, `before${line - 1} =`)).toBe(true);
  }
});

modelIndependentWebE2e("split historical diffs align CodeMirror panes and load the last pair directly", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "split-reader-scroll", name: "Split reader scroll", seed: root => writeFileSync(path.join(root, "split.ts"), "source\n"),
  });
  const text = (name: string) => Array.from({ length: 1500 }, (_, i) => `const ${name}${i} = '${i === 0 && name === "after" ? "wrapped ".repeat(40) : i}';\n`).join("");
  await openReaderViewer(page, project.id, { kind: "text", path: "split.ts", before: text("before"), after: text("after") });
  await page.getByTestId("diff-viewer-split").click();
  const reader = page.getByTestId("source-reader").filter({ visible: true });
  await expect(reader.locator(".cm-editor")).toHaveCount(2);
  const panes = reader.locator(".cm-scroller");
  await expect(panes.nth(0)).toContainText("before0");
  await expect(panes.nth(1)).toContainText("after0");
  await expect.poll(() => reader.evaluate(element => {
    const panes = element.querySelectorAll(".cm-editor");
    const left = panes[0]!.querySelector('[data-source-row="1"]');
    const right = panes[1]!.querySelector('[data-source-row="1501"]');
    return left && right ? Math.abs(left.getBoundingClientRect().top - right.getBoundingClientRect().top) : Infinity;
  })).toBeLessThan(1);
  const height = await panes.nth(1).evaluate(element => element.scrollHeight);
  expect(height).toBeGreaterThan(20_000);
  await page.screenshot({ path: testInfo.outputPath("split-historical-start.png") });
  await panes.nth(1).evaluate(element => { element.scrollTop = element.scrollHeight; });
  await expect(panes.nth(0)).toContainText("before1499");
  await expect(panes.nth(1)).toContainText("after1499");
  expect(Math.abs(await panes.nth(0).evaluate(element => element.scrollHeight) - await panes.nth(1).evaluate(element => element.scrollHeight))).toBeLessThan(2);
  await expect.poll(() => panes.nth(1).evaluate(element => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThan(50);
  expect(Math.abs(await panes.nth(1).evaluate(element => element.scrollHeight) - height)).toBeLessThan(100);
  await page.screenshot({ path: testInfo.outputPath("split-historical-end.png") });
});

modelIndependentWebE2e("switching summarized historical diffs never paints the full-file prefix", async ({ page, request }) => {
  const text = (version: number) => `unchanged top marker\n${"context\n".repeat(1000)}version ${version}\n${"tail\n".repeat(1000)}`;
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "historical-switch", name: "Historical switch", seed: root => writeFileSync(path.join(root, "switch.txt"), text(0)),
  });
  const { apiUrl, token } = apiConfig();
  const source = `${apiUrl}/v1/projects/${project.id}/source`;
  const options = { headers: { Authorization: `Bearer ${token}` }, params: { root_id: project.roots[0]!.id, path: "switch.txt" } };
  for (const version of [1, 2]) await saveRetainedFixture(request, source, options, text(version));
  await page.getByTestId("files-tree-file").filter({ hasText: "switch.txt" }).click();
  await page.getByTestId("file-version-trigger").click();
  const menu = page.getByRole("menu", { name: "Version history" });
  await expect.poll(() => menu.getByRole("menuitemradio").count()).toBeGreaterThan(2);
  await menu.getByRole("menuitemradio").last().click();
  const history = page.getByTestId("file-version-file").filter({ visible: true });
  await expect(history.locator(".cm-content")).toContainText("version 0");
  await expect(history.locator(".cm-content")).not.toContainText("unchanged top marker");
  await page.route("**/source/views/*/presentations/*/rows?*", async route => {
    await new Promise(resolve => setTimeout(resolve, 150));
    await route.continue();
  });
  const documentStage = history.locator("xpath=ancestor::*[contains(@class, 'den-files-editor__document-stage')][1]");
  await documentStage.evaluate(element => {
    const check = () => {
      if ([...element.querySelectorAll<HTMLElement>(".cm-content")].some(node => node.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }) && node.textContent?.includes("unchanged top marker"))) element.setAttribute("data-full-prefix-painted", "true");
    };
    const observer = new MutationObserver(check);
    observer.observe(element, { subtree: true, childList: true, characterData: true });
    Object.assign(element, { stopPaintObservation: () => observer.disconnect() });
  });
  await page.getByTestId("file-version-trigger").click();
  const versions = menu.getByRole("menuitemradio");
  await versions.nth(await versions.count() - 2).click();
  await expect(history.locator(".cm-content")).toContainText("version 1");
  await expect(documentStage).not.toHaveAttribute("data-full-prefix-painted", "true");
  await documentStage.evaluate(element => (element as HTMLElement & { stopPaintObservation(): void }).stopPaintObservation());
});

async function openReaderViewer(page: Page, projectId: string, reader: TextComparisonSource): Promise<void> {
  await page.evaluate(async ({ projectId, reader }) => {
    const viewerModule = "/src/platform/navigation/in-app-diff.ts", readerModule = "/src/api/source-reader.ts";
    const connectionModule = "/src/platform/connection/app-connection.ts", prefsModule = "/src/settings/appearance/display-prefs.ts";
    const [{ openDiffViewer }, { sourceReaderAccess }, { getLycaonClient }, { saveDiffWordWrap, saveDiffSplit }] = await Promise.all([
      import(viewerModule), import(readerModule), import(connectionModule), import(prefsModule),
    ]);
    await saveDiffWordWrap(true);
    await saveDiffSplit(false);
    openDiffViewer({ projectId, path: reader.path, change: "changed", reader: sourceReaderAccess(getLycaonClient(), projectId, reader) });
  }, { projectId, reader });
}

modelIndependentWebE2e("compressed source ranges preserve scrollbar extent across distant page loads", async ({ page, request }) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "compressed-source", name: "Compressed source", seed: root => writeFileSync(path.join(root, "large.txt"), "source\n"),
  });
  const text = Array.from({ length: 120_000 }, (_, index) => `line ${index}\n`).join("");
  await openReaderViewer(page, project.id, { kind: "text", path: "large.txt", before: text, after: text });
  const reader = page.getByTestId("source-reader").filter({ visible: true });
  const scroller = reader.locator(".cm-scroller");
  await expect(reader.locator(".cm-content")).toContainText("line 0");
  await expect.poll(() => scroller.evaluate(element => element.scrollHeight)).toBeGreaterThan(1_900_000);
  const height = await scroller.evaluate(element => element.scrollHeight);
  for (const fraction of [1, 0.5, 0.05, 1, 0]) {
    await scroller.evaluate((element, fraction) => { element.scrollTop = (element.scrollHeight - element.clientHeight) * fraction; }, fraction);
    if (fraction === 1) {
      // Scroll-past-end can clip the final source row above the terminal empty line.
      await expect.poll(() => scroller.evaluate(element => {
        const bounds = element.getBoundingClientRect();
        const last = [...element.querySelectorAll(".cm-line")].find(line => line.textContent === "line 119999");
        const row = last?.getBoundingClientRect();
        const endGap = element.scrollHeight - element.clientHeight - element.scrollTop;
        const measured = {
          atEnd: Math.abs(endGap) <= 1,
          visible: !!row && Math.min(row.bottom, bounds.bottom) > Math.max(row.top, bounds.top),
          endGap, rowTop: row?.top, rowBottom: row?.bottom,
          viewportTop: bounds.top, viewportBottom: bounds.bottom,
        };
        return measured.atEnd && measured.visible ? "aligned" : JSON.stringify(measured);
      })).toBe("aligned");
    } else {
      await expect.poll(() => scroller.evaluate(element => {
        const bounds = element.getBoundingClientRect();
        return [...element.querySelectorAll(".cm-line")].some(line => line.textContent?.startsWith("line ")
          && line.getBoundingClientRect().top >= bounds.top && line.getBoundingClientRect().bottom <= bounds.bottom);
      })).toBe(true);
    }
    await expect.poll(() => scroller.evaluate(element => element.scrollHeight)).toBeGreaterThan(height - 100);
    expect(await scroller.evaluate(element => element.scrollHeight)).toBeLessThan(height + 100);
  }
  await expect(reader.locator(".cm-line").first()).toContainText("line 0");
  expect(await reader.locator(".cm-line").count()).toBeLessThan(200);
});

modelIndependentWebE2e("large historical replacements keep continuous inline shading", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "historical-highlights", name: "Historical highlights", seed: root => writeFileSync(path.join(root, "policy.md"), "# Policy\n"),
  });
  const before = "# Policy\n" + "Read applicable nested policy before backend work.\n".repeat(80);
  const after = "# Policy\n" + "Keep verification receipts available during review.\n".repeat(80);
  await openReaderViewer(page, project.id, { kind: "text", path: "policy.md", before, after });
  const reader = page.getByTestId("source-reader").filter({ visible: true });
  const first = reader.locator('[data-source-row="1"]');
  await expect(first).toContainText("Read applicable nested policy before backend work.");
  await expect(first.locator(".cm-deletedText")).toHaveCount(1);
  await expect(first.locator(".cm-deletedText")).toContainText("Read applicable nested policy before backend work");
  await page.screenshot({ path: testInfo.outputPath("historical-highlights.png") });
});

modelIndependentWebE2e("switching editable Review diffs publishes only their condensed presentation", async ({ page, request }) => {
  const text = (name: string, changed: boolean) => `unchanged review prefix\n${"context\n".repeat(300)}${name} ${changed ? "updated" : "original"}\n${"tail\n".repeat(300)}`;
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "review-condensed-switch", name: "Review condensed switch", seed: root => {
      for (const name of ["first", "second"]) writeFileSync(path.join(root, `${name}.txt`), text(name, false));
    },
  });
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  await expect.poll(async () => {
    const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/workspace`, { headers });
    expect(response.ok()).toBe(true);
    return (await response.json()).inventory.complete;
  }, { timeout: 30_000 }).toBe(true);
  const source = `${apiUrl}/v1/projects/${project.id}/source`;
  for (const name of ["first", "second"]) {
    await saveRetainedFixture(request, source, {
      headers, params: { path: `${name}.txt`, root_id: project.roots[0]!.id },
    }, text(name, true));
  }
  await page.evaluate(id => {
    (window as unknown as { __harness: { openReviewLens(id: string, scope: { kind: string }): void } }).__harness.openReviewLens(id, { kind: "new" });
  }, project.id);
  await page.getByTestId("sidebar-scope-picker").click();
  await page.getByTestId("sidebar-scope-mark-my-edits-control").check();
  await page.getByTestId("sidebar-scope-picker").click();
  const rows = page.getByTestId("review-lens").getByTestId("changes-row-file");
  await rows.filter({ hasText: "first.txt" }).click();
  const editor = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(editor).toContainText("first updated");
  await expect(editor.locator(".cm-foldPlaceholder").first()).toBeVisible();
  await expect(editor.locator(".cm-content")).not.toContainText("unchanged review prefix");
  await page.route("**/source/comparison?*", async route => {
    await new Promise(resolve => setTimeout(resolve, 150));
    await route.continue();
  });
  await page.evaluate(() => {
    const capture = () => {
      for (const content of document.querySelectorAll<HTMLElement>('[data-testid="files-editor-host"] .cm-content')) {
        if (content.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }) && content.textContent?.includes("unchanged review prefix")) {
          document.body.dataset.reviewPrefixPainted = "true";
        }
      }
      document.body.dataset.reviewPaintFrame = String(requestAnimationFrame(capture));
    };
    capture();
  });
  await rows.filter({ hasText: "second.txt" }).click();
  await expect(editor).toContainText("second updated");
  await expect(editor.locator(".cm-content")).toBeEditable();
  await expect(editor.locator(".cm-foldPlaceholder").first()).toBeVisible();
  await expect(page.locator("body")).not.toHaveAttribute("data-review-prefix-painted", "true");
  await page.evaluate(() => cancelAnimationFrame(Number(document.body.dataset.reviewPaintFrame)));
});
