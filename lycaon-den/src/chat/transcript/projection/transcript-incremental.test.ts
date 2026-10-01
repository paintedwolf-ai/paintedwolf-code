import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";

describe("incremental transcript projection", () => {
  const message = (id: string, content: string, ord: number): Message => ({
    id, content, ord, role: "assistant", origin: "model", authority: "none", trust_tier: "trusted", created_at: "t",
  });
  const toolResult = (id: string, assistant: string, call: string, tool: string, ord: number, extra: Partial<NonNullable<Message["tool_result"]>> = {}): Message => ({
    id, role: "tool", origin: "tool", authority: "none", trust_tier: "untrusted",
    content: `${tool} complete`, created_at: "t", ord,
    tool_result: { assistant_message_id: assistant, tool_call_id: call, tool, content: `${tool} complete`, outcome: "completed", ...extra },
  });
  const verbose = { verboseMode: true } as const;
  const spanOf = (rows: readonly Message[]) => [messagesToTranscriptItems(rows, verbose)];

  it("keeps settled spans stable while another span streams and preserves reordering", () => {
    const project = createTranscriptDisplayProjector();
    const first = message("first", "settled", 1);
    const live = message("live", "a", 2);
    const spans = [messagesToTranscriptItems([first]), messagesToTranscriptItems([live])];
    const initial = project([first, live], spans);
    const next = project([first, { ...live, content: "ab" }], spans);
    expect(next[0]).toBe(initial[0]);
    expect(next[1]).not.toBe(initial[1]);
    expect(next[1]).toMatchObject([{ key: "live", text: "ab" }]);
    const reordered = project([first, live], [spans[1]!, spans[0]!]);
    expect(reordered.map((span) => span[0]?.key)).toEqual(["live", "first"]);
    expect(project([], [])).toEqual([]);
  });

  it("refreshes a settled span when its tool result arrives later", () => {
    const project = createTranscriptDisplayProjector();
    const assistant: Message = { ...message("a", "Working", 1), tool_calls: [{ id: "call", name: "read", args: { path: "a.go" } }] };
    const spans = spanOf([assistant]);
    const initial = project([assistant], spans, verbose);
    const result = toolResult("result", "a", "call", "read", 2);
    const next = project([assistant, result], spans, verbose);
    expect(next[0]).not.toBe(initial[0]);
    expect(next[0]).toEqual(createTranscriptDisplayProjector()([assistant, result], [spans[0]!], verbose)[0]!);
    expect(JSON.stringify(next[0])).toContain("read complete");
  });

  it("keeps every unchanged row's identity inside a span that is still streaming", () => {
    const project = createTranscriptDisplayProjector();
    const assistant: Message = { ...message("a", "Working", 1), tool_calls: [{ id: "call", name: "read", args: { path: "a.go" } }] };
    const result = toolResult("result", "a", "call", "read", 2);
    const live = message("live", "a", 3);
    const first = project([assistant, result, live], spanOf([assistant, result, live]), verbose)[0]!;
    const grown = { ...live, content: "ab" };
    const second = project([assistant, result, grown], spanOf([assistant, result, grown]), verbose)[0]!;
    expect(second).not.toBe(first);
    expect(second.length).toBe(first.length);
    const before = new Map(first.map((row) => [row.key, row]));
    for (const row of second) {
      if (row.key === "live") expect(row).not.toBe(before.get("live"));
      else expect(row).toBe(before.get(row.key));
    }
    expect(second.find((row) => row.key === "live")).toMatchObject({ text: "ab" });
  });

  it("keeps a diff row's folds while the turn's prose streams", () => {
    const project = createTranscriptDisplayProjector();
    const user: Message = { ...message("u", "change it", 1), role: "user", origin: "user", authority: "user" };
    const write: Message = { ...message("w", "", 2), tool_calls: [{ id: "call", name: "write", args: { path: "a.go" } }] };
    const result = toolResult("result", "w", "call", "write", 3, {
      file_edit_preview: fileEditPreviewFixture({ path: "a.go", before: "x\n", after: "y\n" }),
    });
    const live = message("live", "Done", 4);
    const diffRow = (rows: readonly { kind: string }[]) => rows.find((row) => row.kind === "file_edit");
    const first = project([user, write, result, live], spanOf([user, write, result, live]), verbose)[0]!;
    expect(diffRow(first)).toBeDefined();
    const grown = { ...live, content: "Done." };
    const second = project([user, write, result, grown], spanOf([user, write, result, grown]), verbose)[0]!;
    expect(diffRow(second)).toBe(diffRow(first));
  });

  it("does not reuse a projection after artifact or visibility changes", () => {
    const project = createTranscriptDisplayProjector();
    const user: Message = { ...message("u", "", 1), role: "user", origin: "user", authority: "user", artifact_ids: ["a"] };
    const first = project([user], undefined);
    const changed = project([{ ...user, artifact_ids: ["b"] }], undefined);
    expect(changed[0]).not.toBe(first[0]);
    expect(changed[0]![0]).not.toBe(first[0]![0]);
    expect(changed[0]).toMatchObject([{ artifactIds: ["b"] }]);
    const draft: Message = { ...message("d", "draft", 2), kind: "draft", draft_status: "live" };
    const verboseRows = project([draft], undefined, { verboseMode: true });
    const normal = project([draft], undefined, { verboseMode: false });
    expect(verboseRows[0]?.length).toBe(1);
    expect(normal[0]).toEqual([]);
  });
});
