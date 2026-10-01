import { expect } from "@playwright/test";
import { appStateStorageSliceKey } from "../shared/app-state-storage.ts";
import { writeFileSync } from "node:fs";
import path from "node:path";
import {
  bootstrapChatSession,
  liveChatStage,
  sendChatPrompt,
  waitForChatComposerReady,
  settledChatSessionId,
  openProjectFilesFixture,
  webE2e,
} from "./helpers.ts";

/**
 * Web-tier Add to chat:
 * - Drop a File onto the chat area → upload → chip → send carries the blob handle.
 * - addToChat(path-file) → chip (desktop native path-ref is harness/manual).
 */
webE2e.describe("add to chat", () => {
  webE2e("editor selection stages one range label and removes the intended chip", async ({ page, request }) => {
    await openProjectFilesFixture(page, request, {
      prefix: "selection-attachment", name: "Selection attachment",
      seed: (root) => writeFileSync(path.join(root, "café.ts"), "const first = 1;\nconst second = 2;\nconst third = 3;"),
    });
    await page.getByTestId("files-tree-file").filter({ hasText: "café.ts" }).click();
    const editor = page.getByTestId("files-editor-host").locator(".cm-content").filter({ visible: true });
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await editor.press("ControlOrMeta+a");
    await editor.click({ button: "right" });
    await page.getByRole("menuitem", { name: /^Add to chat/ }).click();
    await page.getByRole("dialog", { name: "Choose a chat" }).getByRole("button").filter({ hasText: "Current chat" }).click();
    const chip = liveChatStage(page).getByTestId("composer-attachment-chip");
    await expect(chip.locator(".den-composer-chip-label")).toHaveText("café.ts:1–3");
    await chip.getByRole("button", { name: "Remove café.ts:1–3", exact: true }).click();
    await expect(chip).toHaveCount(0);
  });

  webE2e("drop a File onto the chat area → restart → chip → send carries the part", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await waitForChatComposerReady(page);
    const chat = liveChatStage(page).getByTestId("chat-view");
    await expect(chat).toBeVisible({ timeout: 90_000 });

    const uploadWait = page.waitForResponse(
      (res) =>
        res.request().method() === "POST" &&
        /\/v1\/projects\/[^/]+\/attachments(?:\?|$)/.test(res.url()),
      { timeout: 20_000 },
    );
    const acceptedDrop = await chat.evaluate((el) => {
      const file = new File(
        [new TextEncoder().encode("hello from drop\n")],
        "drop-note.txt",
        { type: "text/plain" },
      );
      const dt = new DataTransfer();
      dt.items.add(file);
      el.dispatchEvent(new DragEvent("dragenter", { bubbles: true, cancelable: true, dataTransfer: dt }));
      el.dispatchEvent(new DragEvent("dragover", { bubbles: true, cancelable: true, dataTransfer: dt }));
      const affordance = el.querySelector('[data-testid="chat-drop-overlay"]')?.textContent;
      const accepted = !el.dispatchEvent(
        new DragEvent("drop", {
          bubbles: true,
          cancelable: true,
          dataTransfer: dt,
        }),
      );
      return { accepted, affordance, files: dt.files.length };
    });
    expect(acceptedDrop, "the visible chat must accept an available file drop").toMatchObject({
      accepted: true, files: 1, affordance: "Drop to add to chat",
    });
    const uploadResponse = await uploadWait;
    expect(uploadResponse.ok()).toBe(true);
    const uploadRequest = uploadResponse.request();
    expect(new URL(uploadRequest.url()).searchParams.get("filename")).toBe("drop-note.txt");
    const receipt = (await uploadResponse.json()) as {
      blob_id: string;
      filename: string;
      mime: string;
    };
    expect(receipt).toMatchObject({
      filename: "drop-note.txt",
      mime: "text/plain",
    });
    expect(receipt.blob_id).toMatch(/^[0-9a-f]{64}$/);

    const chip = liveChatStage(page).getByTestId("composer-attachment-chip");
    await expect(chip).toBeVisible({ timeout: 10_000 });
    await expect(chip).toHaveAttribute("data-kind", "text");

    await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), appStateStorageSliceKey("composerAttachments"))).toContain(receipt.blob_id);
    await page.reload();
    await expect(chip).toBeVisible({ timeout: 30_000 });
    await expect(chip).toContainText("drop-note.txt");

    const promptWait = page.waitForRequest(
      (req) =>
        req.method() === "POST" &&
        /\/v1\/sessions\/[^/]+\/prompts$/.test(req.url()),
    );
    await sendChatPrompt(page, "see the drop");
    const promptReq = await promptWait;
    const body = promptReq.postDataJSON() as {
      attachments?: Array<{ blob_id?: string }>;
    };
    expect(body.attachments).toEqual([{ blob_id: receipt.blob_id }]);
  });

  webE2e("Add to chat from the sink → path-file chip", async ({ page, request }) => {
    const project = await bootstrapChatSession(page, request);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
      timeout: 90_000,
    });

    const rootId = project.roots[0]?.id;
    const sessionId = await settledChatSessionId(page);
    expect(rootId).toBeTruthy();

    const sinkUrl = "/src/chat/composer/add-to-chat.ts";
    const result = await page.evaluate(
      async ({ projectId, sessionId: sid, rootId: rid, url }) => {
        const mod = (await import(/* @vite-ignore */ url)) as {
          addToChat: (ref: {
            kind: "path-file";
            projectId: string;
            rootId: string;
            path: string;
            name: string;
          }, options: {
            destination: { projectId: string; sessionId: string };
          }) => Promise<{ ok: true } | { ok: false; reason: string }>;
        };
        return mod.addToChat(
          {
            kind: "path-file",
            projectId,
            rootId: rid,
            path: "README.md",
            name: "README.md",
          },
          { destination: { projectId, sessionId: sid } },
        );
      },
      { projectId: project.id, sessionId, rootId: rootId!, url: sinkUrl },
    );
    expect(result).toMatchObject({ ok: true });

    const chip = liveChatStage(page).getByTestId("composer-attachment-chip");
    await expect(chip).toBeVisible({ timeout: 10_000 });
    await expect(chip).toHaveAttribute("data-kind", "path-file");
    await expect(chip).toContainText("README.md");
  });

  webE2e("selection range chip → prompt references carry start/end lines", async ({
    page,
    request,
  }) => {
    const project = await bootstrapChatSession(page, request);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
      timeout: 90_000,
    });

    const rootId = project.roots[0]?.id;
    const sessionId = await settledChatSessionId(page);
    expect(rootId).toBeTruthy();

    const sinkUrl = "/src/chat/composer/add-to-chat.ts";
    const result = await page.evaluate(
      async ({ projectId, sessionId: sid, rootId: rid, url }) => {
        const mod = (await import(/* @vite-ignore */ url)) as {
          addToChat: (ref: {
            kind: "path-file";
            projectId: string;
            rootId: string;
            path: string;
            name: string;
            startLine?: number;
            endLine?: number;
          }, options: {
            destination: { projectId: string; sessionId: string };
          }) => Promise<{ ok: true } | { ok: false; reason: string }>;
        };
        return mod.addToChat(
          {
            kind: "path-file",
            projectId,
            rootId: rid,
            path: "src/main.ts",
            name: "main.ts:12–34",
            startLine: 12,
            endLine: 34,
          },
          { destination: { projectId, sessionId: sid } },
        );
      },
      { projectId: project.id, sessionId, rootId: rootId!, url: sinkUrl },
    );
    expect(result).toMatchObject({ ok: true });

    const chip = liveChatStage(page).getByTestId("composer-attachment-chip");
    await expect(chip).toBeVisible({ timeout: 10_000 });
    await expect(chip).toContainText("main.ts:12–34");

    const promptWait = page.waitForRequest(
      (req) =>
        req.method() === "POST" &&
        /\/v1\/sessions\/[^/]+\/prompts$/.test(req.url()),
    );
    await sendChatPrompt(page, "ground this range");
    const promptReq = await promptWait;
    const body = promptReq.postDataJSON() as {
      references?: Array<{
        kind?: string;
        path?: string;
        start_line?: number;
        end_line?: number;
      }>;
    };
    expect(body.references?.[0]).toMatchObject({
      kind: "path-file",
      path: "src/main.ts",
      start_line: 12,
      end_line: 34,
    });
  });
});
