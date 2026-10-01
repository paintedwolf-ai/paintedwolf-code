import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import type { Message, ProgressStep } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { sortTranscriptItemsByOrd } from "./transcript-item-order.ts";

describe("transcript-items", () => {

  function promotionMessage(): Message {
    return {
      id: "merge-result", ord: 8, role: "tool", origin: "host",
      authority: "none", trust_tier: "trusted", content: "", created_at: "t8",
      tool_result: {
        content: "", outcome: "completed",
        promotion_previews: [
          fileEditPreviewFixture({ root_id: "root-1", path: "README.md", before: "primary", after: "merged" }),
          fileEditPreviewFixture({ root_id: "root-1", path: "new.md", before: null, after: "created" }),
        ],
      },
    };
  }

  it("orders merged diffs at the promotion result across arrival orders", () => {
    const messages: Message[] = [
      { id: "dispatch", ord: 2, role: "tool", origin: "tool", authority: "none",
        trust_tier: "untrusted", content: "", created_at: "t2",
        tool_result: { content: "", dispatch: { worker_id: "job-1" } } },
      { id: "before", ord: 7, role: "assistant", origin: "model", authority: "none",
        trust_tier: "trusted", content: "Reviewing the overlay", created_at: "t7" },
      promotionMessage(),
      { id: "after", ord: 9, role: "assistant", origin: "model", authority: "none",
        trust_tier: "trusted", content: "Merged", created_at: "t9" },
    ];
    for (const arrivals of [messages, [...messages].reverse()]) {
      const rows = createTranscriptDisplayProjector()(arrivals, undefined)[0]!;
      expect(rows.map((row) => row.key)).toEqual(["before", "worker-file-edit:merge-result", "after"]);
      const edit = rows.find((row) => row.kind === "worker_file_edit");
      expect(edit).toMatchObject({
        anchorMessageId: "merge-result",
        folds: [
          { net: fileEditPreviewFixture({ root_id: "root-1", path: "README.md", before: "primary", after: "merged" }) },
          { net: fileEditPreviewFixture({ root_id: "root-1", path: "new.md", before: null, after: "created" }) },
        ],
      });
    }
  });

  it("does not infer merged diffs from completion text or an empty promotion", () => {
    const message = promotionMessage();
    message.tool_result = { content: '[host:overlay-promote-event] {"merge_status":"merged"}' };
    expect(createTranscriptDisplayProjector()([message], undefined)[0]!).toEqual([]);
    message.tool_result.promotion_previews = [];
    expect(createTranscriptDisplayProjector()([message], undefined)[0]!).toEqual([]);
  });

  it("sortTranscriptItemsByOrd orders by immutable ord, not wire ts or arrival index", () => {
    const sharedTs = "2026-01-01T00:00:02.000Z";
    const messages = [
      { id: "u1", ord: 1, role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "2026-01-01T00:00:00.000Z" },
      {
        id: "a1",
        ord: 2,
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "thinking",
        created_at: sharedTs,
        tool_calls: [{ id: "tc1", name: "grep", args: { pattern: "main" } }],
      },
      {
        id: "t1",
        ord: 3,
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "matches",
        tool_result: { content: "matches", tool: "grep", tool_call_id: "tc1", assistant_message_id: "a1" },
        created_at: sharedTs,
      },
      {
        id: "pu1",
        ord: 4,
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "progress_update" as const,
        progress_update: {
          steps: [{ state: "pending", label: "Plan" }] satisfies ProgressStep[],
          initial: true,
          seq: 1,
        },
        created_at: sharedTs,
      },
    ];
    // Tied timestamps leave ord (2/3/4) as the ordering fact.
    const items = sortTranscriptItemsByOrd(messagesToTranscriptItems(messages), messages);
    const keys = items.map((item) => item.key);
    expect(keys.indexOf("a1")).toBeLessThan(keys.indexOf("a1:tc1"));
    expect(keys.indexOf("a1:tc1")).toBeLessThan(keys.indexOf("pu1"));
  });
});
