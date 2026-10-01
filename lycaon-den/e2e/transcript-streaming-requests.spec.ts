import { expect } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

const SOURCE_VIEW_READ = /\/v1\/projects\/[^/]+\/source\/views\/[^/?]+(\?|$)/;

webE2e(
  "rows arriving after a settled diff row leave its comparison alone",
  async ({ page, request }) => {
    await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    // Two writes to one path: the row composes a range, so it asks the comparison for its summary once.
    const writes = [
      { before: "", after: "export const value = 1;\n" },
      { before: "export const value = 1;\n", after: "export const value = 2;\n" },
    ];
    await apiSeedSessionTranscript(
      request,
      sessionId,
      transcriptMessages([
        { id: crypto.randomUUID(), role: "user", content: "write the module", seq: 1, ord: 1 },
        ...writes.flatMap((write, index) => {
          const assistantId = crypto.randomUUID();
          const toolCallId = `tc-streaming-write-${index}`;
          const seq = 2 + index * 2;
          return [
            {
              id: assistantId,
              role: "assistant" as const,
              content: "",
              tool_calls: [{ id: toolCallId, name: "write", args: { path: "src/streamed.ts", content: write.after } }],
              seq,
              ord: seq,
            },
            {
              id: crypto.randomUUID(),
              role: "tool" as const,
              content: "wrote src/streamed.ts",
              tool_result: {
                content: "wrote src/streamed.ts",
                tool: "write",
                tool_call_id: toolCallId,
                assistant_message_id: assistantId,
                file_edit: { path: "src/streamed.ts", ...write },
              },
              seq: seq + 1,
              ord: seq + 1,
            },
          ];
        }),
      ]),
    );
    await expect(page.getByTestId("file-edit-diff")).toBeAttached({ timeout: 15_000 });
    await expect(page.getByTestId("file-edit-diff-writes")).toHaveText("2 edits", { timeout: 15_000 });
    await expect(page.getByTestId("file-edit-diff-stat")).toBeAttached({ timeout: 15_000 });

    // The diff row has its summary; later rows in the same turn must not ask for it again.
    const reads: string[] = [];
    page.on("request", (sent) => {
      if (sent.method() === "GET" && SOURCE_VIEW_READ.test(sent.url())) reads.push(sent.url());
    });
    const arrivals = 12;
    for (let step = 1; step <= arrivals; step += 1) {
      await apiSeedSessionTranscript(
        request,
        sessionId,
        transcriptMessages([
          { id: crypto.randomUUID(), role: "assistant", content: `streamed reply ${step}`, seq: 5 + step, ord: 5 + step },
        ]),
      );
    }
    await expect(page.getByTestId("message-stream").getByText(`streamed reply ${arrivals}`).first()).toBeAttached({ timeout: 15_000 });
    await page.waitForTimeout(500);
    expect(reads, reads.join("\n")).toHaveLength(0);
  },
);
