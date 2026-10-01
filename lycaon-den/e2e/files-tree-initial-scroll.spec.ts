import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import {
  activateProject,
  apiCreateProjectWithRoot,
  e2eTempDir,
  e2eUniqueLabel,
  gotoShell,
  openProjectFilesFixture,
  webE2e,
} from "./helpers.ts";

webE2e("tree browsing keeps focus; the editor focus command transfers scrolling", async ({
  page,
  request,
}) => {
  await openProjectFilesFixture(page, request, {
    prefix: "files-editor-keyboard-focus",
    name: "Editor keyboard focus",
    seed: (root) => {
      writeFileSync(
        path.join(root, "keyboard-scroll.ts"),
        Array.from(
          { length: 240 },
          (_, index) => `export const line${index} = ${index};`,
        ).join("\n"),
      );
    },
  });

  const row = page
    .getByTestId("files-tree-file")
    .filter({ hasText: "keyboard-scroll.ts" });
  await row.click();
  const content = page.getByTestId("files-editor-host").locator(".cm-content");
  await expect(content).toHaveAttribute("contenteditable", "true");
  await expect(row).toBeFocused();

  await page.keyboard.press("ControlOrMeta+;");
  await page.keyboard.press("t");
  await expect(content).toBeFocused();
  await page.keyboard.press("PageDown");
  const scroller = page.getByTestId("files-editor-host").locator(".cm-scroller");
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
});

webE2e("files tree scrolls before a file is opened", async ({
  page,
  request,
}) => {
  const root = e2eTempDir(request, "files-tree-initial-scroll");
  for (let index = 0; index < 160; index += 1) {
    writeFileSync(
      path.join(root, `file-${String(index).padStart(3, "0")}.ts`),
      `export const value = ${index};\n`,
    );
  }
  const project = await apiCreateProjectWithRoot(
    request,
    root,
    e2eUniqueLabel("Files tree initial scroll"),
  );

  await gotoShell(page);
  await activateProject(page, project.id);
  await page.getByTestId("project-files-entry").click();

  const tree = page.getByTestId("files-tree");
  const scroller = page.getByTestId("files-tree-scroll");
  await expect(tree).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("files-editor-empty")).toBeVisible();
  await expect(tree.locator(".den-files-tree__row--active")).toHaveCount(1);

  const geometry = await scroller.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
  }));
  expect(geometry.scrollHeight).toBeGreaterThan(geometry.clientHeight);

  const verticalScrollbar = page
    .getByTestId("files-tree-scroll-frame")
    .locator(":scope > .os-scrollbar-vertical");
  await expect(verticalScrollbar).toHaveClass(/os-scrollbar-visible/);
  await expect(verticalScrollbar).not.toHaveClass(/os-scrollbar-unusable/);

  await scroller.hover();
  await expect(verticalScrollbar).not.toHaveClass(/den-scrollbar-idle/, {
    timeout: 500,
  });
  await page.mouse.wheel(0, 600);
  await expect
    .poll(() => scroller.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
});

webE2e("File summary keeps an independent wheel scrollport", async ({
  page,
  request,
}, testInfo) => {
  const longSymbol =
    "TestBuildLedgerFromTranscript_enriches_wire_shapes_without_overflow";
  const root = e2eTempDir(request, "file-summary-scroll");
  writeFileSync(
    path.join(root, "long-summary.ts"),
    `export const ${longSymbol} = 1;\n`,
  );
  const project = await apiCreateProjectWithRoot(
    request,
    root,
    e2eUniqueLabel("File summary scroll"),
  );
  await page.route("**/v1/projects/*/source/file-briefings**", async (route) => {
    const url = new URL(route.request().url());
    const body = route.request().method() === "POST"
      ? route.request().postDataJSON() as { root_id?: string; path?: string }
      : null;
    const rootId = url.searchParams.get("root_id") ?? body?.root_id ?? "root";
    const filePath = url.searchParams.get("path") ?? body?.path ?? "long-summary.ts";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        target_key: "summary-scroll-target",
        attempt_id: "11111111-1111-4111-8111-111111111111",
        root_id: rootId,
        path: filePath,
        presentation: "current",
        status: "complete",
        preview: { language: "typescript", line_count: 1 },
        locations: [{ line: 1, name: longSymbol, kind: "variable" }],
        sections: [
          {
            kind: "purpose",
            text: `Defines the \`${longSymbol}\` fixture. ${Array.from(
              { length: 80 },
              (_, index) => `Scrollable summary paragraph ${index + 1}.`,
            ).join(" ")}`,
          },
        ],
        fallback_text: "",
        truncated: false,
        source_sha256: "summary-scroll-sha",
        updated_at: "2026-08-24T00:00:00Z",
      }),
    });
  });

  await gotoShell(page);
  await activateProject(page, project.id);
  await page.getByTestId("project-files-entry").click();
  await expect(page.getByTestId("files-tree")).toBeVisible({ timeout: 15_000 });
  await page.getByTestId("files-tree-file").click();
  await page.getByTestId("file-summary-toggle").click();

  const summary = page.getByTestId("file-summary-scroll").locator(":scope > .den-scrollport__viewport");
  await expect(page.getByTestId("file-summary-sections")).toBeVisible();
  const symbol = page.getByRole("button", {
    name: `Open ${longSymbol} at line 1`,
  });
  await expect(symbol).toBeVisible();
  await expect(summary).toBeVisible();
  const geometry = await summary.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
    clientWidth: element.clientWidth,
    scrollWidth: element.scrollWidth,
  }));
  expect(geometry.scrollHeight).toBeGreaterThan(geometry.clientHeight);
  expect(geometry.scrollWidth).toBeLessThanOrEqual(geometry.clientWidth);
  await expect(symbol).toHaveCSS("white-space", "normal");

  await summary.hover();
  const diagnosticPath = testInfo.outputPath("summary-wheel-target.json");
  writeFileSync(diagnosticPath, JSON.stringify(await summary.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const target = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
      const ancestors = [];
      for (let current = target; current; current = current.parentElement) {
        const style = getComputedStyle(current);
        ancestors.push({
          tag: current.tagName, className: current.className,
          overflow: style.overflow, overscroll: style.overscrollBehavior,
          height: current.clientHeight, scrollHeight: current.scrollHeight,
        });
      }
      return { rect: rect.toJSON(), ancestors, attributes: [...element.attributes].map((attribute) => [attribute.name, attribute.value]), children: [...element.children].map((child) => ({ tag: child.tagName, className: child.className, attributes: [...child.attributes].map((attribute) => [attribute.name, attribute.value]) })) };
    })));
  await testInfo.attach("Summary wheel target", {
    contentType: "application/json",
    path: diagnosticPath,
  });
  await page.mouse.wheel(0, 600);
  await expect
    .poll(() => summary.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
});
