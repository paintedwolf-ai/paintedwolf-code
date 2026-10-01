import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("editor activation scopes its reactive computations", async ({ page, request }) => {
  const warnings: string[] = [];
  await page.addInitScript(() => {
    const warn = console.warn.bind(console);
    console.warn = (...args: unknown[]) => {
      if (args.some((value) => String(value).includes("computations created outside"))) {
    warn(...args, new Error("Reactive computation trace").stack);
      } else {
        warn(...args);
      }
    };
  });
  page.on("console", (message) => {
    if (message.type() === "warning" && message.text().includes("computations created outside")) {
      warnings.push(message.text());
    }
  });
  await openProjectFilesFixture(page, request, {
    prefix: "editor-reactive-lifetime",
    name: "Editor reactive lifetime",
    seed: (root) => {
      writeFileSync(path.join(root, "first.md"), "# First document\n");
      writeFileSync(path.join(root, "second.md"), "# Second document\n");
    },
  });
  for (const name of ["first.md", "second.md", "first.md"]) {
    await page.getByTestId("files-tree-file").filter({ hasText: name }).click();
    await expect(page.locator(".den-files-editor__status:visible")).toHaveAttribute("data-editor-identity", "editing");
    await page.getByRole("button", { name: "Project configuration", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Project configuration", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Close project configuration", exact: true }).click();
    await expect(page.locator(".den-files-editor__status:visible")).toHaveAttribute("data-editor-identity", "editing");
  }
  expect(warnings).toEqual([]);
});
