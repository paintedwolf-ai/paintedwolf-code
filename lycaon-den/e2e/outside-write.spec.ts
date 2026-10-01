import { mkdirSync, renameSync, statSync, unlinkSync, utimesSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiConfig, openProjectFilesFixture, webE2e } from "./helpers.ts";

// An edit made outside the app reaches an open, clean tab on its own: the
// host observes the write, re-reads the document, and the editor converges.
webE2e("outside write converges into an open clean tab", async ({ page, request }) => {
  const { root } = await openProjectFilesFixture(page, request, {
    prefix: "outside-write",
    name: "Outside write",
    seed: (dir) => writeFileSync(path.join(dir, "notes.md"), "first draft\n"),
  });

  await page.getByTestId("files-tree-file").filter({ hasText: "notes.md" }).click();
  const host = page.getByTestId("files-editor-host");
  await expect(host.locator(".cm-content")).toContainText("first draft");

  writeFileSync(path.join(root, "notes.md"), "rewritten outside\n");

  await expect(host.locator(".cm-content")).toContainText("rewritten outside", {
    timeout: 15_000,
  });
  await expect(host.locator(".cm-content")).not.toContainText("first draft");
  await expect(page.getByTestId("files-editor-status-note")).toHaveText("Updated to latest");
});

webE2e("outside changes update file discovery and code search including hidden and ignored files", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "outside-discovery", name: "Outside discovery",
    seed: (dir) => {
      mkdirSync(path.join(dir, ".hidden"));
      mkdirSync(path.join(dir, "generated"));
      writeFileSync(path.join(dir, ".gitignore"), "generated/\n");
      for (const rel of ["plain.txt", ".hidden/a.txt", "generated/b.txt"]) {
        writeFileSync(path.join(dir, rel), "DiscoveryNeedleBefore\n");
      }
    },
  });
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const search = async (needle: string) => {
    const response = await request.post(`${apiUrl}/v1/search`, {
      headers, data: { query: `kind:code path:* ${needle}`, origin_project_id: project.id, limit: 20 },
    });
    expect(response.ok(), await response.text()).toBe(true);
    const result = await response.json() as { hits: { path?: string }[] };
    return [...new Set(result.hits.map((hit) => hit.path))].sort();
  };
  await expect.poll(() => search("DiscoveryNeedleBefore"), { timeout: 30_000 }).toEqual([".hidden/a.txt", "generated/b.txt", "plain.txt"]);
  const before = statSync(path.join(root, ".hidden/a.txt"));
  writeFileSync(path.join(root, ".hidden/a.txt"), "DiscoveryNeedleAfter!\n");
  utimesSync(path.join(root, ".hidden/a.txt"), before.atime, before.mtime);
  writeFileSync(path.join(root, ".hidden/c.txt"), "DiscoveryNeedleAfter!\n");
  renameSync(path.join(root, "plain.txt"), path.join(root, "moved.txt"));
  unlinkSync(path.join(root, "generated/b.txt"));
  await expect(page.locator('[data-testid="files-tree-file"][data-path="moved.txt"]')).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('[data-testid="files-tree-file"][data-path="plain.txt"]')).toHaveCount(0);
  await expect.poll(() => search("DiscoveryNeedleAfter"), { timeout: 30_000 }).toEqual([".hidden/a.txt", ".hidden/c.txt"]);
  await expect.poll(() => search("DiscoveryNeedleBefore"), { timeout: 30_000 }).toEqual(["moved.txt"]);
});

webE2e("outside directory replacement refreshes an open descendant", async ({ page, request }) => {
  const { root } = await openProjectFilesFixture(page, request, {
    prefix: "outside-directory",
    name: "Outside directory",
    seed: (dir) => {
      mkdirSync(path.join(dir, "src"));
      writeFileSync(path.join(dir, "src", "notes.md"), "original directory\n");
      mkdirSync(path.join(dir, "incoming"));
      writeFileSync(path.join(dir, "incoming", "notes.md"), "replacement directory\n");
    },
  });
  await page.locator('[data-testid="files-tree-dir"][data-path="src"]').getByRole("button", { name: "Expand src", exact: true }).click();
  await page.getByTestId("files-tree-file").filter({ hasText: "notes.md" }).click();
  const content = page.getByTestId("files-editor-host").locator(".cm-content");
  await expect(content).toContainText("original directory");

  renameSync(path.join(root, "src"), path.join(root, "previous"));
  renameSync(path.join(root, "incoming"), path.join(root, "src"));

  await expect(content).toContainText("replacement directory", { timeout: 15_000 });
});
