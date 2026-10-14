// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import type { ToolPartView } from "../../tool/tool-part-model.ts";
import { mockScrollerMotion } from "../../../test/scroll-mock.ts";
import { createTranscriptVirtualizer } from "./transcript-virtualizer.ts";
import { transcriptRowContentRevision } from "./transcript-row-revision.ts";

const part = (id: string, overrides: Partial<ToolPartView> = {}): ToolPartView => ({
  id,
  toolCallId: id,
  assistantMessageId: "a1",
  messageId: id,
  tool: "read",
  kind: "read",
  status: "completed",
  ...overrides,
});
const span = (parts: ToolPartView[]): TranscriptItem => ({
  kind: "activity_span",
  key: parts[0]!.id,
  label: "reading",
  entries: parts.map((p) => ({ kind: "tool" as const, part: p })),
});

describe("transcript row content revision", () => {
  it("is the same for equal content in a new row object", () => {
    const item: TranscriptItem = { kind: "assistant", key: "a", text: "Done." };
    expect(transcriptRowContentRevision(item)).toBe(transcriptRowContentRevision(item));
    expect(transcriptRowContentRevision(item))
      .toBe(transcriptRowContentRevision({ kind: "assistant", key: "a", text: "Done." }));
  });

  it("changes when an activity span gains an entry under the same key", () => {
    const short = transcriptRowContentRevision(span([part("b"), part("c")]));
    expect(transcriptRowContentRevision(span([part("b"), part("c"), part("d")]))).not.toBe(short);
  });

  it("changes when a tool settles with output", () => {
    const running = transcriptRowContentRevision(span([part("b", { status: "running" })]));
    const settled = transcriptRowContentRevision(span([part("b", { output: "x".repeat(4_000) })]));
    expect(settled).not.toBe(running);
  });

  it("changes when a reply's text changes", () => {
    expect(transcriptRowContentRevision({ kind: "assistant", key: "a", text: "Draft" }))
      .not.toBe(transcriptRowContentRevision({ kind: "assistant", key: "a", text: "Draft, revised" }));
  });

  it("covers all row kinds", () => {
    const items = [
      { kind: "user", key: "u", text: "hi", artifactIds: ["art-1"] },
      { kind: "pending_user", key: "pu", text: "pending", pending: {} as never },
      { kind: "fallback", key: "fb", text: "fallback", role: "system" },
      { kind: "draft", key: "d", text: "draft", draftStatus: "live", draftVersionCount: 2 },
      { kind: "tool", key: "t", part: part("t", { error: "err", fileEdit: { path: "a", added: 1, removed: 2 } as never }) },
      {
        kind: "activity_span",
        key: "as",
        label: "work",
        entries: [
          { kind: "turn_load", key: "tl", row: { load: { tokens: 10 } } as never },
          { kind: "index_warming", key: "iw", meta: { status: "ready" } as never },
        ],
      },
      { kind: "worker_group", key: "wg", parts: [part("w1")] },
      {
        kind: "file_edit",
        key: "fe",
        anchorMessageId: "m1",
        folds: [{ key: "f1", path: "a.ts", steps: [], net: { added: 2, removed: 1 } as never }],
      },
      { kind: "progress_complete", key: "pc", steps: [{ label: "step 1", state: "done" }] },
      {
        kind: "progress_update",
        key: "pu2",
        initial: true,
        steps: [],
        changes: [],
        summary: { change_count: 1, total_steps: 1, pending: 0, done: 1, na: 0 },
      },
      { kind: "workflow_feedback", key: "wf", meta: { prompt: "q" } as never },
      { kind: "blueprint_card", key: "bc", content: "# plan", meta: {} as never, view: { revisionKey: "r1", phase: "review", collapsed: false } as never },
      { kind: "workflow_boundary", key: "wb", label: "done" },
      { kind: "turn_load", key: "tl2", row: { load: { tokens: 5 } } as never },
      { kind: "time_marker", key: "tm", variant: "day", at: 100 },
      { kind: "unread_marker", key: "um", seenAt: 200 },
      { kind: "turn_tail", key: "tt", anchorMessageId: "a", settledAt: 300, activeMs: 10, workMs: 20 },
    ] as unknown as TranscriptItem[];
    for (const item of items) {
      expect(transcriptRowContentRevision(item)).toMatch(/^[0-9a-z]+\.[0-9a-z]+$/);
    }
  });

  it("re-estimates an unmounted row when its content revision changes", () => {
    const el = document.createElement("div");
    Object.defineProperty(el, "clientHeight", { value: 200, configurable: true });
    Object.defineProperty(el, "clientWidth", { value: 400, configurable: true });
    Object.defineProperty(el, "scrollHeight", { value: 10_000, configurable: true });
    Object.defineProperty(el, "scrollTop", { value: 0, writable: true, configurable: true });
    mockScrollerMotion(el);
    document.body.appendChild(el);

    const [items, setItems] = createSignal<TranscriptItem[]>([
      { kind: "assistant", key: "top", text: "short" },
      ...Array.from({ length: 20 }, (_, i): TranscriptItem => ({
        kind: "user",
        key: `u-${i}`,
        text: `msg-${i}`,
      })),
    ]);

    createRoot((dispose) => {
      try {
        const virtualizer = createTranscriptVirtualizer({
          items,
          scrollElement: () => el,
          contentRevision: transcriptRowContentRevision,
          rowSizeHint: (item) => (item.kind === "assistant" && item.key === "top" ? (item.text === "short" ? 80 : 320) : 72),
          revealOffset: () => {},
          shiftContent: (delta) => delta,
          observeElementRect: (_instance, cb) => { cb({ width: 400, height: 200 }); return () => {}; },
          observeElementOffset: (_instance, cb) => { cb(1_200, false); return () => {}; },
        });
        virtualizer.resizeItems([{ index: 0, size: 80 }]);
        const before = virtualizer.getTotalSize();

        // Update unmounted row "top" with new content under the same key.
        setItems([
          { kind: "assistant", key: "top", text: "expanded content" },
          ...items().slice(1),
        ]);

        expect(virtualizer.getTotalSize()).toBe(before + 240);
      } finally {
        dispose();
        el.remove();
      }
    });
  });
});
