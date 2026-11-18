import { expect } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  sendChatPrompt,
  settledChatSessionId,
  transcriptMessages,
  waitForChatComposerReady,
  webE2e,
} from "./helpers.ts";

webE2e("repeated uploads stage and send one attachment", async ({ page, request }, testInfo) => {
  await bootstrapChatSession(page, request);
  await waitForChatComposerReady(page);
  const chat = liveChatStage(page).getByTestId("chat-view");
  await expect(chat).toBeVisible();
  const receipts: string[] = [];
  for (let index = 0; index < 2; index++) {
    const uploaded = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      /\/v1\/projects\/[^/]+\/attachments(?:\?|$)/.test(response.url()),
    );
    await chat.evaluate((element) => {
      const transfer = new DataTransfer();
      transfer.items.add(new File(["same content"], "note.txt", { type: "text/plain" }));
      for (const type of ["dragenter", "dragover", "drop"]) {
        element.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: transfer }));
      }
    });
    const response = await uploaded;
    expect(response.ok()).toBe(true);
    receipts.push((await response.json() as { blob_id: string }).blob_id);
  }
  expect(receipts[1]).toBe(receipts[0]);
  const chips = liveChatStage(page).getByTestId("composer-attachment-chips");
  await expect(chips.getByTestId("composer-attachment-chip")).toHaveCount(1);
  await testInfo.attach("One chip for two uploads", { body: await chips.screenshot(), contentType: "image/png" });

  const posting = page.waitForRequest((sent) =>
    sent.method() === "POST" && /\/v1\/sessions\/[^/]+\/prompts$/.test(sent.url()),
  );
  await sendChatPrompt(page, "Read the note.");
  expect((await posting).postDataJSON().attachments).toEqual([{ blob_id: receipts[0] }]);
});

webE2e("an outside edit path opens its retained comparison", async ({ page, request }, testInfo) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const path = "/external/report.txt";
  const assistantId = crypto.randomUUID();
  const callId = "outside-edit";
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "user", content: "Update the report", seq: 1, ord: 1 },
    {
      id: assistantId, role: "assistant", content: "", seq: 2, ord: 2,
      tool_calls: [{ id: callId, name: "write", args: { path, content: "new report\n" } }],
    },
    {
      id: crypto.randomUUID(), role: "tool", content: "Report updated", seq: 3, ord: 3,
      tool_result: {
        content: "Report updated", tool: "write", tool_call_id: callId,
        assistant_message_id: assistantId,
        file_edits: [{ path, before: "old report\n", after: "new report\n" }],
      },
    },
  ]));
  const card = page.getByTestId("file-edit-diff").filter({ hasText: path });
  await expect(card.getByTestId("source-path-link")).toHaveCount(0);
  await card.getByText(path, { exact: true }).click();
  await expect(card).toHaveAttribute("data-expanded", "true");
  await expect(card.getByText("new report", { exact: true })).toBeVisible();
  await testInfo.attach("Outside path with its retained diff", { body: await card.screenshot(), contentType: "image/png" });
});
