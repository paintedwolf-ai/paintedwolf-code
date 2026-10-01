import { execFile } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  apiConfig,
  bootstrapChatSession,
  liveChatStage,
  sendChatPrompt,
  settledChatSessionId,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

type PendingRestore = {
  staging_dir: string;
  recovery_dir: string;
};

function readPendingRestore(dir: string): PendingRestore {
  const markerPath = path.join(dir, "restore.pending.json");
  const marker = JSON.parse(fs.readFileSync(markerPath, "utf8")) as PendingRestore;
  for (const [kind, candidate] of [
    ["staging", marker.staging_dir],
    ["recovery", marker.recovery_dir],
  ] as const) {
    expect(path.dirname(candidate)).toBe(dir);
    expect(path.basename(candidate)).toMatch(
      new RegExp(`^\\.restore-${kind}-[0-9a-f-]{36}$`),
    );
  }
  return marker;
}

async function downloadBackup(
  request: APIRequestContext,
): Promise<Buffer> {
  const { apiUrl, token } = apiConfig();
  const res = await request.get(`${apiUrl}/v1/backup`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.ok(), await res.text()).toBeTruthy();
  const disposition = res.headers()["content-disposition"] ?? "";
  const match = /filename="([^"]+)"/.exec(disposition);
  expect(match?.[1]).toMatch(/\.zip$/);
  const bytes = Buffer.from(await res.body());
  expect(bytes.subarray(0, 2).toString("utf8")).toBe("PK");
  expect(bytes.includes(Buffer.from("manifest.json"))).toBeTruthy();
  expect(bytes.includes(Buffer.from("store.db"))).toBeTruthy();
  return bytes;
}

async function openDataPanel(page: Page) {
  await page
    .getByTestId("shell-nav-dock")
    .getByRole("button", { name: "Settings" })
    .click();
  await expect(page.getByTestId("settings-view")).toBeVisible({
    timeout: 15_000,
  });
  await page.getByTestId("settings-nav-debug").click();
  await page.getByTestId("advanced-tab-data").click();
  await expect(page.getByTestId("advanced-panel-data")).toBeVisible();
  await expect(page.getByTestId("data-settings-panel")).toBeVisible();
}

webE2e.describe("backup export den", () => {
  webE2e(
    "Data tab explains secret exclusion and stages restore with recovery copy",
    async ({ page, request }) => {
      webE2e.setTimeout(360_000);
      await bootstrapChatSession(page, request);
      await openDataPanel(page);

      await expect(page.getByTestId("data-backup-secrets-note")).toContainText(
        "Device credentials, MCP connections, and extension setup are excluded",
      );
      await expect(page.getByTestId("data-backup-secrets-note")).not.toContainText(
        /lycaon/i,
      );
      await expect(page.getByTestId("data-backup-now")).toBeVisible();
      await expect(page.getByTestId("data-restore")).toBeVisible();

      const bytes = await downloadBackup(request!);
      const [chooser] = await Promise.all([
        page.waitForEvent("filechooser"),
        page.getByTestId("data-restore").click(),
      ]);
      await chooser.setFiles({
        name: "painted-wolf-backup.zip",
        mimeType: "application/zip",
        buffer: bytes,
      });
      await expect(page.getByTestId("confirm-destructive-dialog")).toBeVisible();
      await expect(page.getByTestId("confirm-destructive-dialog")).toContainText(
        /replace.*data/i,
      );
      const stagedResponse = page.waitForResponse(
        (response) => response.url().endsWith("/v1/backup/restore") &&
          response.request().method() === "POST",
      );
      await page.getByTestId("confirm-destructive-ok").click();
      const response = await stagedResponse;
      expect(response.ok(), await response.text()).toBeTruthy();
      const staged = await response.json() as {
        restart_required: boolean;
        recovery_copy_path: string;
      };
      const dir = path.dirname(staged.recovery_copy_path);
      try {
        expect(staged.restart_required).toBe(true);
        const pending = readPendingRestore(dir);
        expect(pending.recovery_dir).toBe(staged.recovery_copy_path);
        expect(fs.existsSync(path.join(pending.staging_dir, "store.db"))).toBeTruthy();
        await expect(page.getByTestId("data-restore-staged")).toContainText(
          "Restart Painted Wolf Code",
          { timeout: 15_000 },
        );
        await expect(page.getByTestId("data-restore-staged")).toContainText(
          path.basename(pending.recovery_dir),
        );
        const { apiUrl, token } = apiConfig();
        const blocked = await request!.get(`${apiUrl}/v1/projects`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        expect(blocked.status()).toBe(409);
        expect(await blocked.json()).toMatchObject({ code: "backup_restore_pending" });
      } finally {
        const restart = fileURLToPath(new URL("../../scripts/harness/crash-restart.sh", import.meta.url));
        await promisify(execFile)("bash", [restart], { timeout: 300_000 });
      }
      expect(fs.existsSync(path.join(dir, "restore.pending.json"))).toBe(false);
      expect(fs.existsSync(staged.recovery_copy_path)).toBe(true);
      await page.reload();
      await waitHarnessConnected(page);
      const { apiUrl, token } = apiConfig();
      const projects = await request!.get(`${apiUrl}/v1/projects`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      expect(projects.ok(), await projects.text()).toBeTruthy();
    },
  );

  webE2e("exports session transcript as Markdown and JSON", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page, request);
    await sendChatPrompt(page, "hello backup export");
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeEnabled({
      timeout: 90_000,
    });
    const sessionId = await settledChatSessionId(page);
    expect(sessionId).toBeTruthy();

    const { apiUrl, token } = apiConfig();
    for (const format of ["md", "json"] as const) {
      const res = await request!.get(
        `${apiUrl}/v1/sessions/${sessionId}/export?format=${format}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      expect(res.ok(), await res.text()).toBeTruthy();
      const disposition = res.headers()["content-disposition"] ?? "";
      expect(disposition).toMatch(/filename="/);
      const match = /filename="([^"]+)"/.exec(disposition);
      expect(match?.[1]).toMatch(new RegExp(`\\.${format === "md" ? "md" : "json"}$`));
      const body = await res.text();
      if (format === "md") {
        expect(body.length).toBeGreaterThan(0);
      } else {
        const pageJson = JSON.parse(body) as { messages?: unknown[] };
        expect(Array.isArray(pageJson.messages)).toBeTruthy();
      }
    }

    await page.getByTestId("session-export-menu-trigger").click();
    const downloadPromise = page.waitForEvent("download");
    await page.getByTestId("session-export-md").click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toMatch(/\.md$/);
  });
});
