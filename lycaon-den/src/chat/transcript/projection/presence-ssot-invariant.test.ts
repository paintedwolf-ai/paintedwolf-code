import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";

describe("presence SSOT invariant", () => {
  const baseMessages: Message[] = [
    { id: "u1", ord: 1, role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
    {
      id: "slot",
      ord: 2,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "draft prose",
      visibility: "internal",
      draft_status: "live",
      status: "complete",
      created_at: "t",
    },
    {
      id: "a2",
      ord: 3,
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "final",
      visibility: "transcript",
      status: "complete",
      created_at: "t",
    },
  ];

  const keys = (items: readonly { key: string }[]) =>
    items.map((i) => i.key);

  it("toggling llmActive adds or removes zero rows", () => {
    const baseline = keys(createTranscriptDisplayProjector()(baseMessages, undefined)[0]!);
    const withLlm = keys(
      createTranscriptDisplayProjector()(baseMessages, undefined, { llmActive: true } as never)[0]!,
    );
    const withoutLlm = keys(
      createTranscriptDisplayProjector()(baseMessages, undefined, { llmActive: false } as never)[0]!,
    );
    expect(withLlm).toEqual(baseline);
    expect(withoutLlm).toEqual(baseline);
  });

  it("streaming status changes draft live flag without changing keys", () => {
    const settled = messagesToTranscriptItems(baseMessages);
    const streaming = messagesToTranscriptItems(
      baseMessages.map((m) =>
        m.id === "slot" ? { ...m, status: "streaming" as const } : m,
      ),
    );
    expect(settled.map((i) => i.key)).toEqual(streaming.map((i) => i.key));
    const settledDraft = settled.find((i) => i.kind === "draft");
    const streamingDraft = streaming.find((i) => i.kind === "draft");
    expect(settledDraft?.kind).toBe("draft");
    expect(streamingDraft?.kind).toBe("draft");
    if (settledDraft?.kind === "draft" && streamingDraft?.kind === "draft") {
      expect(settledDraft.live).toBe(false);
      expect(streamingDraft.live).toBe(true);
    }
  });

  it("verbose toggle changes visibility as a pure function of the setting", () => {
    const withWarming: Message[] = [
      ...baseMessages,
      {
        id: "warm",
        ord: 4,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "index_warming",
        content: "warming",
        visibility: "transcript",
        status: "complete",
        index_warming: { trigger: "manual" },
        created_at: "t",
      },
    ];
    const quiet = messagesToTranscriptItems(withWarming, { verboseMode: false });
    const verbose = messagesToTranscriptItems(withWarming, { verboseMode: true });
    expect(quiet.some((i) => i.key === "warm")).toBe(false);
    expect(verbose.some((i) => i.key === "warm")).toBe(true);
    // Same setting → same keys (deterministic).
    expect(keys(messagesToTranscriptItems(withWarming, { verboseMode: false }))).toEqual(
      keys(quiet),
    );
  });

  it("keeps draft visibility stable across live-to-committed transition", () => {
    const live = baseMessages.slice(0, 2);
    const committed: Message[] = [
      live[0]!,
      {
        ...live[1]!,
        kind: "draft",
        visibility: "transcript",
        draft_status: "committed",
        status: "complete",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "README.md" } }],
      },
    ];
    const draftKeys = (messages: readonly Message[], verboseMode: boolean) =>
      createTranscriptDisplayProjector()(messages, undefined, { verboseMode })[0]!
        .filter((item) => item.kind === "draft")
        .map((item) => item.key);

    expect(draftKeys(live, false)).toEqual([]);
    expect(draftKeys(committed, false)).toEqual([]);
    expect(draftKeys(live, true)).toEqual(["slot"]);
    expect(draftKeys(committed, true)).toEqual(["slot"]);
  });

  it("hydrated draft_status=live without status=streaming renders settled", () => {
    const stuck: Message[] = Array.from({ length: 8 }, (_, i) => ({
      id: `stuck-${i}`,
      ord: i + 1,
      role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      kind: "draft" as const,
      content: `body ${i}`,
      visibility: "transcript" as const,
      draft_status: "live" as const,
      status: "complete" as const,
      tool_calls: [{ id: `tc-${i}`, name: "read", args: {} }],
      draft_version_count: 1,
      created_at: "t",
    }));
    const items = messagesToTranscriptItems(stuck);
    const drafts = items.filter((i) => i.kind === "draft");
    expect(drafts.length).toBeGreaterThan(0);
    for (const d of drafts) {
      if (d.kind === "draft") expect(d.live).toBe(false);
    }
  });

  it("checkpoint row-child presence follows tool_result.checkpoint_decision", () => {
    const without: Message[] = [
      {
        id: "a1",
        ord: 1,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1", name: "write", args: {} }],
        status: "complete",
        created_at: "t",
      },
    ];
    const withDecision: Message[] = [
      {
        ...without[0]!,
      },
      {
        id: "t1",
        ord: 2,
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        tool_result: {
          tool: "write",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          content: "ok",
          checkpoint_decision: {
            checkpoint_id: "cp1",
            status: "pending",
            kind: "tool_approval",
          },
        },
        status: "complete",
        created_at: "t",
      },
    ];
    const before = messagesToTranscriptItems(without);
    const after = messagesToTranscriptItems(withDecision);
    expect(before.some((i) => i.kind === "checkpoint")).toBe(false);
    expect(after.some((i) => i.kind === "checkpoint")).toBe(true);
  });
});
