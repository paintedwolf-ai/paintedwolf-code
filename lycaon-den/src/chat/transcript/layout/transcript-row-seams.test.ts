import { describe, expect, it } from "vitest";
import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";
import { transcriptRowSeams, transcriptSeamBelow } from "./transcript-row-seams.ts";

const messages = ["user", "pending_user", "assistant", "fallback"] as const;
const cards = ["draft", "workflow_feedback", "workflow_explain", "progress_complete", "progress_update",
  "tool", "activity_span", "worker_group", "worker_file_edit", "file_edit", "blueprint_card",
  "checkpoint", "workflow_boundary", "turn_tail"] as const;
type Kind = DisplayTranscriptItem["kind"];
const row = (kind: Kind, key: string) => ({ kind, key });

describe("transcript row seams", () => {
  it("spaces adjacent visible edges and external tail slots", () => {
    for (const upper of [...messages, ...cards]) {
      for (const lower of [...messages, ...cards]) {
        for (const actions of [false, true]) {
          for (const card of [false, true]) {
            const context = { hasActions: () => actions, closesWithCard: () => card };
            const rows = [row(upper, "upper"), row(lower, "lower")];
            const seams = transcriptRowSeams(rows, context);
            const upperMessage = messages.some((kind) => kind === upper) && !card;
            const lowerMessage = messages.some((kind) => kind === lower);
            const afterUpper = upperMessage ? actions ? "message-reserved" : "message" : "item";
            expect(seams.has("upper")).toBe(false);
            expect(seams.get("lower")).toBe(upperMessage || !lowerMessage ? afterUpper : "message");
            expect(transcriptSeamBelow(rows.slice(0, 1), context)).toBe(afterUpper);
          }
        }
      }
    }
    expect(transcriptSeamBelow([])).toBeUndefined();
  });

  it("places markers with the row they date", () => {
    const markers = [row("time_marker", "day"), row("unread_marker", "unread")];
    const rows = [row("user", "prompt"), ...markers, row("tool", "activity")];
    expect([...transcriptRowSeams(rows, { hasActions: () => true })].sort()).toEqual([
      ["activity", "item"], ["day", "message-reserved"], ["unread", "item"],
    ]);
    expect(transcriptRowSeams(markers).has("day")).toBe(false);
    expect(transcriptRowSeams(rows.slice(0, -1)).get("day")).toBe("message");
    expect(transcriptSeamBelow(markers)).toBe("item");
  });
});
