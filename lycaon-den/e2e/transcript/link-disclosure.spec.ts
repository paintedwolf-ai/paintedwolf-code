import { expect, type Locator } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  liveChatStage,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "../helpers.ts";

type LinkGeometry = {
  card: { left: number; width: number };
  label: { left: number; top: number; right: number };
  chevronLeft: number;
  cardInset: number;
};

async function linkGeometry(card: Locator): Promise<LinkGeometry> {
  return card.evaluate((element) => {
    const header = element.querySelector(":scope > .den-transcript-disclosure-header")!;
    const label = header.firstElementChild!.getBoundingClientRect();
    const box = element.getBoundingClientRect();
    return {
      card: { left: box.left, width: box.width },
      label: { left: label.left, top: label.top, right: label.right },
      chevronLeft: header.lastElementChild!.getBoundingClientRect().left,
      cardInset: parseFloat(getComputedStyle(element).getPropertyValue("--den-transcript-card-inset-x")),
    };
  });
}

/** Toggles the card and waits for its label to finish sliding to `labelLeft`. */
async function toggleAndSettle(card: Locator, expanded: boolean, labelLeft: number): Promise<LinkGeometry> {
  await card.locator(":scope > .den-transcript-disclosure-header").click();
  await expect(card).toHaveAttribute("data-expanded", String(expanded));
  await expect(card).not.toHaveAttribute("data-animating", /.*/);
  await expect.poll(async () => Math.abs((await linkGeometry(card)).label.left - labelLeft)).toBeLessThan(0.5);
  return linkGeometry(card);
}

webE2e(
  "a link disclosure rests on the text edge and opens into a card without moving its label vertically",
  async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const assistantId = crypto.randomUUID();
    const after = "export const value = 1;\n";
    await apiSeedSessionTranscript(
      request,
      sessionId,
      transcriptMessages([
        { id: crypto.randomUUID(), role: "user", content: "write the module", seq: 1, ord: 1 },
        {
          id: assistantId,
          role: "assistant",
          content: "",
          tool_calls: [{ id: "tc-link-write", name: "write", args: { path: "src/linked.ts", content: after } }],
          seq: 2,
          ord: 2,
        },
        {
          id: crypto.randomUUID(),
          role: "tool",
          content: "wrote src/linked.ts",
          tool_result: {
            content: "wrote src/linked.ts",
            tool: "write",
            tool_call_id: "tc-link-write",
            assistant_message_id: assistantId,
            file_edits: [{ path: "src/linked.ts", before: "", after }],
          },
          seq: 3,
          ord: 3,
        },
        {
          id: crypto.randomUUID(),
          role: "assistant",
          content: "The module is written.",
          status: "complete",
          seq: 4,
          ord: 4,
          grounding: {
            traced: true,
            cited_evidence: [
              { handle: "read#1", path: "cmd/fixture/main.go", line: 9, excerpt: "func main()", verdict: "matched" },
            ],
          },
        },
      ]),
    );

    const stage = liveChatStage(page);
    const prose = stage.locator(".bubble--assistant").last();
    await expect(prose).toBeVisible({ timeout: 30_000 });
    const streamWidth = await stage
      .locator(".den-chat-transcript-visual")
      .evaluate((element) => element.getBoundingClientRect().width);
    const textEdge = await prose.evaluate((element) => element.getBoundingClientRect().left);

    for (const card of [stage.getByTestId("diff-group"), stage.getByTestId("citation-evidence-chicklet")]) {
      await expect(card).toBeVisible({ timeout: 30_000 });
      const resting = await linkGeometry(card);
      expect(resting.label.left).toBeCloseTo(textEdge, 0);
      expect(resting.card.width).toBeLessThan(streamWidth / 2);
      expect(resting.chevronLeft).toBeGreaterThanOrEqual(resting.label.right);

      const open = await toggleAndSettle(card, true, textEdge + resting.cardInset);
      expect(open.card.left).toBeCloseTo(textEdge, 0);
      expect(open.card.width).toBeCloseTo(streamWidth, 0);
      expect(open.label.left).toBeCloseTo(textEdge + open.cardInset, 0);
      expect(open.label.top).toBeCloseTo(resting.label.top, 0);
      expect(open.chevronLeft).toBeGreaterThanOrEqual(open.label.right);

      const closed = await toggleAndSettle(card, false, textEdge);
      expect(closed.label.left).toBeCloseTo(textEdge, 0);
      expect(closed.label.top).toBeCloseTo(resting.label.top, 0);
      expect(closed.card.width).toBeCloseTo(resting.card.width, 0);
    }
  },
);

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
  const group = liveChatStage(page).getByTestId("diff-group");
  await group.getByTestId("diff-group-files").click();
  const card = group.getByTestId("file-edit-diff").filter({ hasText: path });
  const label = card.getByText(path, { exact: true });
  await expect(card).toHaveAttribute("data-expanded", "true");
  await expect(card.getByTestId("source-path-link")).toHaveCount(0);
  await label.click();
  await expect(card).toHaveAttribute("data-expanded", "false");
  await label.click();
  await expect(card).toHaveAttribute("data-expanded", "true");
  await expect(card.getByText("new report", { exact: true })).toBeVisible();
  await testInfo.attach("Outside path with its retained diff", { body: await card.screenshot(), contentType: "image/png" });
});
