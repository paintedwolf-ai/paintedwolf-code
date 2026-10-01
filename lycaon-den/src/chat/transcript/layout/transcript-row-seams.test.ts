import { describe, expect, it } from "vitest";
import { transcriptRowSeams } from "./transcript-row-seams.ts";
import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";

const user = (key: string): DisplayTranscriptItem => ({ kind: "user", key, text: "ask" });
const prose = (key: string): DisplayTranscriptItem => ({ kind: "assistant", key, text: "answer" });
const span = (key: string): DisplayTranscriptItem =>
  ({ kind: "activity_span", key, label: "Read files", entries: [] });
const diff = (key: string): DisplayTranscriptItem =>
  ({ kind: "file_edit", key, folds: [], anchorMessageId: key });
const day = (key: string): DisplayTranscriptItem =>
  ({ kind: "time_marker", key, variant: "day", at: 0, anchorMessageId: key });
const unread = (key: string): DisplayTranscriptItem =>
  ({ kind: "unread_marker", key, seenAt: 0, anchorMessageId: key });
const tail = (key: string): DisplayTranscriptItem => ({
  kind: "turn_tail",
  key,
  openingMessageId: "u1",
  anchorMessageId: key,
  startedAt: 0,
  settledAt: 1,
  activeMs: 1,
  workMs: 1,
});
const pending = (key: string, kind: "prompt" | "queue_send"): DisplayTranscriptItem => ({
  kind: "pending_user",
  key,
  text: "queued",
  pending: { kind, operationId: key, text: "queued", createdAt: 0, state: "sending" },
});

describe("transcript row seams", () => {
  it("gives the first row no seam and the turn rung to every later prompt", () => {
    const seams = transcriptRowSeams([user("u1"), prose("a1"), user("u2"), prose("a2")]);

    expect(seams.get("u1")).toBeUndefined();
    expect(seams.get("a1")).toBe("section");
    expect(seams.get("u2")).toBe("turn");
    expect(seams.get("a2")).toBe("section");
  });

  it("keeps consecutive rows of one kind on the row rung", () => {
    const seams = transcriptRowSeams([user("u1"), span("s1"), span("s2"), diff("d1"), diff("d2")]);

    expect(seams.get("s1")).toBe("section");
    expect(seams.get("s2")).toBe("row");
    expect(seams.get("d1")).toBe("section");
    expect(seams.get("d2")).toBe("row");
  });

  it("holds a queue send inside the open turn, unlike an ordinary prompt", () => {
    const continued = transcriptRowSeams(
      [user("u1"), prose("a1"), user("u2")],
      { continuation: (key) => key === "u2" },
    );
    expect(continued.get("u2")).toBe("section");

    const sends = transcriptRowSeams([user("u1"), prose("a1"), pending("p1", "queue_send")]);
    expect(sends.get("p1")).toBe("section");

    const prompts = transcriptRowSeams([user("u1"), prose("a1"), pending("p2", "prompt")]);
    expect(prompts.get("p2")).toBe("turn");
  });

  it("settles the tail with the turn above it and the seam below it", () => {
    const seams = transcriptRowSeams([user("u1"), prose("a1"), tail("t1"), user("u2")]);

    expect(seams.get("t1")).toBe("row");
    expect(seams.get("u2")).toBe("turn");
  });

  it("hands a marker the seam of the row it dates", () => {
    const seams = transcriptRowSeams([user("u1"), prose("a1"), tail("t1"), day("day"), user("u2")]);

    expect(seams.get("day")).toBe("turn");
    expect(seams.get("u2")).toBe("row");
  });

  it("leaves a leading marker without a seam and keeps a marker run tight", () => {
    const seams = transcriptRowSeams([day("day"), unread("unread"), user("u1")]);

    expect(seams.get("day")).toBeUndefined();
    expect(seams.get("unread")).toBe("row");
    expect(seams.get("u1")).toBe("row");
  });

  it("separates a trailing marker from the rows above it", () => {
    const seams = transcriptRowSeams([user("u1"), prose("a1"), unread("unread")]);

    expect(seams.get("unread")).toBe("section");
  });

  it("marks both edges of a catalog span", () => {
    const runs = new Map([["s1", "run-a"], ["s2", "run-a"]]);
    const seams = transcriptRowSeams(
      [user("u1"), span("s1"), span("s2"), span("s3")],
      { spanKeyOf: (key) => runs.get(key) },
    );

    expect(seams.get("s1")).toBe("section");
    expect(seams.get("s2")).toBe("row");
    expect(seams.get("s3")).toBe("section");
  });

  it("never adds a span edge to a turn boundary", () => {
    const seams = transcriptRowSeams(
      [prose("a1"), user("u2"), prose("a2")],
      { spanKeyOf: (key) => (key === "a1" ? undefined : "run-a") },
    );

    // One seam, one rung: the turn wins rather than compounding with the span edge.
    expect(seams.get("u2")).toBe("turn");
  });
});
