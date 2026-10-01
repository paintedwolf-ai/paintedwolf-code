import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import {
  TRANSCRIPT_ROW_COLLAPSED_PRESENTATION,
  transcriptRowPresentationEstimate,
  transcriptRowPresentation,
} from "./transcript-row-presentation.ts";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import {
  transcriptDisclosureKey as disclosure,
  type TranscriptDisclosureKey,
} from "./transcript-disclosure-key.ts";

function tool(id: string): Extract<TranscriptItem, { kind: "tool" }> {
  return {
    kind: "tool",
    key: id,
    part: {
      id,
      toolCallId: id,
      assistantMessageId: "assistant-message",
      messageId: id,
      tool: "read",
      kind: "read",
      status: "completed",
    },
  };
}

describe("transcript row presentation", () => {
  it("separates closed groups, open groups, and their expanded comparisons", () => {
    const item = { kind: "file_edit", key: "edit", folds: [{ key: "a" }, { key: "b" }] } as TranscriptItem;
    const open = new Set<TranscriptDisclosureKey>();
    const read = (key: TranscriptDisclosureKey) => open.has(key);
    open.add(disclosure.diffFile("b"));
    expect(transcriptRowPresentation(item, read)).toBe("collapsed");
    expect(transcriptRowPresentationEstimate(item, read)).toBe(38);
    open.add(disclosure.diffGroup("edit"));
    expect(transcriptRowPresentation(item, read)).toBe(
      JSON.stringify([disclosure.diffGroup("edit"), disclosure.diffFile("b")]),
    );
    expect(transcriptRowPresentationEstimate(item, read)).toBe(474);
    open.delete(disclosure.diffFile("b"));
    expect(transcriptRowPresentation(item, read)).toBe(JSON.stringify([disclosure.diffGroup("edit")]));
    expect(transcriptRowPresentationEstimate(item, read)).toBe(114);
  });

  it("separates the same tool row's collapsed and expanded layouts", () => {
    const item = tool("tool-1");
    const open = new Set<TranscriptDisclosureKey>();
    const isOpen = (key: TranscriptDisclosureKey) => open.has(key);
    expect(transcriptRowPresentation(item, isOpen)).toBe(
      TRANSCRIPT_ROW_COLLAPSED_PRESENTATION,
    );

    open.add(disclosure.tool("tool-1"));
    expect(transcriptRowPresentation(item, isOpen)).toBe(JSON.stringify([disclosure.tool("tool-1")]));
    expect(transcriptRowPresentationEstimate(item, isOpen)).toBe(420);
  });

  it("ignores a remembered child while its activity span is collapsed", () => {
    const child = tool("tool-1");
    const group: Extract<TranscriptItem, { kind: "activity_span" }> = {
      kind: "activity_span",
      key: "tool-1",
      label: "investigating",
      entries: [{ kind: "tool", part: child.part }],
    };
    const open = new Set([disclosure.tool("tool-1")]);
    const isOpen = (key: TranscriptDisclosureKey) => open.has(key);
    expect(transcriptRowPresentation(group, isOpen)).toBe(
      TRANSCRIPT_ROW_COLLAPSED_PRESENTATION,
    );
    expect(transcriptRowPresentationEstimate(group, isOpen)).toBe(64);

    open.add(disclosure.activitySpan("tool-1"));
    expect(transcriptRowPresentation(group, isOpen)).toBe(
      JSON.stringify([disclosure.activitySpan("tool-1"), disclosure.tool("tool-1")]),
    );
    expect(transcriptRowPresentationEstimate(group, isOpen)).toBe(466);
  });

  it("estimates every file-edit run as one collapsed group", () => {
    const fold = (index: number, path: string) => ({
      path,
      key: `write-${index}`,
      net: fileEditPreviewFixture({ path, before: "", after: "content\n" }),
      steps: [
        {
          key: `write-${index}`,
          snapshot: fileEditPreviewFixture({ path, before: "", after: "content\n" }),
          tool: "write",
        },
      ],
    });
    const diffRow = (paths: readonly string[]): Extract<
      TranscriptItem,
      { kind: "file_edit" }
    > => ({
      kind: "file_edit",
      key: "file-edit:write-0",
      folds: paths.map((path, index) => fold(index, path)),
      anchorMessageId: "result-write-0",
    });
    const isOpen = () => false;

    expect(transcriptRowPresentationEstimate(diffRow(["a.ts"]), isOpen)).toBe(38);
    expect(
      transcriptRowPresentationEstimate(diffRow(["a.ts", "b.ts"]), isOpen),
    ).toBe(38);
    expect(
      transcriptRowPresentationEstimate(
        diffRow(["a.ts", "b.ts", "c.ts"]),
        isOpen,
      ),
    ).toBe(38);
  });

  it("tracks open keyed cards and treats other kinds as collapsed", () => {
    const card = {
      kind: "blueprint_card",
      key: "plan-1",
      meta: {
        blueprint_path: "plan.md",
        revision_key: "r1",
        status: "ready",
        phase: "review",
      },
      content: "# plan",
      view: {},
    } as unknown as TranscriptItem;
    const open = new Set<TranscriptDisclosureKey>();
    const isOpen = (key: TranscriptDisclosureKey) => open.has(key);
    open.add(disclosure.tool("plan-1"));
    expect(transcriptRowPresentation(card, isOpen)).toBe(
      TRANSCRIPT_ROW_COLLAPSED_PRESENTATION,
    );
    open.add(disclosure.blueprint("plan-1"));
    expect(transcriptRowPresentation(card, isOpen)).toBe(JSON.stringify([disclosure.blueprint("plan-1")]));

    const user: TranscriptItem = { kind: "user", key: "u1", text: "hi" };
    open.add(disclosure.tool("u1"));
    expect(transcriptRowPresentation(user, isOpen)).toBe(
      TRANSCRIPT_ROW_COLLAPSED_PRESENTATION,
    );
  });
});
