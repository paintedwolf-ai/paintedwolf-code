import { describe, expect, it } from "vitest";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import {
  presentedVisualsHeight,
  proseHeight,
  transcriptRowContentEstimate,
  type PresentedVisual,
} from "./transcript-row-content-estimate.ts";

const metrics = { widthPx: 700, remPx: 14, bodyPx: 13 };
const capture: PresentedVisual = { kind: "image", width: 488, height: 320 };

function assistant(text: string): TranscriptItem {
  return { kind: "assistant", key: "a1", text };
}

describe("transcript row content estimate", () => {
  it("accounts for the pending attachment rail independently of prose", () => {
    const pending = { kind: "prompt" as const, operationId: "op", text: "", createdAt: 0, state: "sending" as const, attachmentLabels: ["report.pdf"] };
    const withChip = transcriptRowContentEstimate({ kind: "pending_user", key: "pending", text: "", pending }, metrics, []);
    const withoutChip = transcriptRowContentEstimate({ kind: "pending_user", key: "pending", text: "", pending: { ...pending, attachmentLabels: [] } }, metrics, []);
    expect(withChip! - withoutChip!).toBe(28);
  });

  it.each([632, 898, 1129])("ignores hidden PDF bodies at width %i while retaining the chip rail", (widthPx) => {
    const prose = "Review this document and explain its conclusions.";
    const user = (length: number): TranscriptItem => ({ kind: "user", key: "pdf", text: prose + "x".repeat(length), contentParts: [
      { content: prose, origin: "user", authority: "user", trust_tier: "trusted" },
      { content: "x".repeat(length), origin: "attachment", authority: "none", trust_tier: "untrusted", source: "report.pdf", media_type: "application/pdf", blob_id: "blob", size_bytes: length },
    ] });
    const rowMetrics = { widthPx, remPx: 14, bodyPx: 14 };
    const short = transcriptRowContentEstimate(user(100), rowMetrics, []);
    const long = transcriptRowContentEstimate(user(17000), rowMetrics, []);
    expect(long).toBe(short);
    expect(long).toBeLessThan(150);
    expect(long).toBeGreaterThan(transcriptRowContentEstimate({ kind: "user", key: "plain", text: prose }, rowMetrics, [])!);
  });

  it("wraps prose to the column width", () => {
    const text = "x".repeat(1_000);

    // 700px at 6.5px per glyph holds 107 characters: ten lines.
    expect(proseHeight(text, 700, 13)).toBeCloseTo(10 * 13 * 1.55);
    expect(proseHeight(text, 1_400, 13)).toBeLessThan(proseHeight(text, 700, 13));
  });

  it("counts a blank line as paragraph spacing", () => {
    expect(proseHeight("one\n\ntwo", 700, 13)).toBeCloseTo(2.5 * 13 * 1.55);
  });

  it("reserves a presented image at its stamped aspect ratio", () => {
    const estimate = transcriptRowContentEstimate(assistant("Done."), metrics, [capture]);

    // 694px frame × 320/488, border, margins, block spacing, and one line.
    const image = 694 * (320 / 488) + 2 + 1.25 * 14 + 0.75 * 14;
    expect(estimate).toBe(Math.round(image + 13 * 1.55));
  });

  it("caps a presented image at the prominent width", () => {
    const wide = presentedVisualsHeight([capture], 1_600, 14);
    const capped = presentedVisualsHeight([capture], 766, 14);

    expect(wide).toBeCloseTo(capped);
  });

  it("lays two presented images side by side in a strip", () => {
    const strip = presentedVisualsHeight([capture, capture], 700, 14);
    const single = presentedVisualsHeight([capture], 700, 14);

    expect(strip).toBeLessThan(single * 2);
  });

  it("holds a placeholder for an image without a stamped size", () => {
    const unsized = presentedVisualsHeight([{ kind: "unsized" }], 700, 14);

    expect(unsized).toBeCloseTo(7.5 * 14 + 2 + 1.25 * 14 + 0.75 * 14);
  });

  it("gives a repeated artifact only its reference chip", () => {
    expect(presentedVisualsHeight([{ kind: "reference" }], 700, 14)).toBeCloseTo(28 + 0.75 * 14);
  });

  it("wraps a user message to its narrower bubble", () => {
    const text = "y".repeat(600);
    const user = transcriptRowContentEstimate({ kind: "user", key: "u1", text }, metrics, []);
    const reply = transcriptRowContentEstimate(assistant(text), metrics, []);

    expect(user).toBeGreaterThan(reply ?? 0);
  });

  it("leaves rows whose content does not set their height to other estimates", () => {
    const boundary = { kind: "workflow_boundary", key: "b1" } as unknown as TranscriptItem;

    expect(transcriptRowContentEstimate(boundary, metrics, [])).toBeUndefined();
    expect(transcriptRowContentEstimate(assistant(""), metrics, [])).toBeUndefined();
  });
});
