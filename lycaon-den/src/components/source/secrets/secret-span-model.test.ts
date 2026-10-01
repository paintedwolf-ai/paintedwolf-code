import { Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import type { SecretScreen } from "../../../api/types.ts";
import {
  secretScreenSummary,
  secretSpanAt,
  secretSpanMarks,
  siblingSecretSpans,
} from "./secret-span-model.ts";

function screen(partial: Partial<SecretScreen>): SecretScreen {
  return { truncated: false, ...partial };
}

describe("secretSpanMarks", () => {
  it("resolves rune offsets onto document positions", () => {
    const doc = Text.of(['const key = "AKIAQYJK5TXV4NZR7SGB";']);
    const marks = secretSpanMarks(
      screen({ spans: [{ start: 13, end: 33, state: "detected", rule_id: "aws" }] }),
      doc,
    );
    expect(marks).toHaveLength(1);
    expect(doc.sliceString(marks[0]!.from, marks[0]!.to)).toBe("AKIAQYJK5TXV4NZR7SGB");
  });

  it("shifts positions past a multi-byte character rather than assuming runes are code units", () => {
    const doc = Text.of(['const emoji = "🔑"; const key = "AKIAQYJK5TXV4NZR7SGB";']);
    // The host counted runes: the emoji is one rune but two UTF-16 units.
    const runes = Array.from(doc.toString());
    const start = runes.indexOf("A");
    const marks = secretSpanMarks(
      screen({ spans: [{ start, end: start + 20, state: "detected", rule_id: "aws" }] }),
      doc,
    );
    expect(doc.sliceString(marks[0]!.from, marks[0]!.to)).toBe("AKIAQYJK5TXV4NZR7SGB");
  });

  it("drops a span the document no longer contains", () => {
    const doc = Text.of(["short"]);
    expect(
      secretSpanMarks(
        screen({ spans: [{ start: 0, end: 400, state: "detected", rule_id: "aws" }] }),
        doc,
      ),
    ).toEqual([]);
  });

  it("returns nothing without a screen", () => {
    expect(secretSpanMarks(null, Text.of(["anything"]))).toEqual([]);
  });
});

describe("secretScreenSummary", () => {
  it("distinguishes an unavailable screen from one with no findings", () => {
    const none = secretScreenSummary(null);
    const empty = secretScreenSummary(screen({ spans: [] }));
    expect(none.gap).toBe("not_screened");
    expect(empty.gap).toBeNull();
    expect(none.total).toBe(0);
    expect(empty.total).toBe(0);
  });

  it("reports truncation as its own gap", () => {
    expect(secretScreenSummary(screen({ truncated: true })).gap).toBe("truncated");
  });

  it("counts each state separately", () => {
    const summary = secretScreenSummary(
      screen({
        spans: [
          { start: 0, end: 9, state: "tracked", rule_id: "managed-secret" },
          { start: 10, end: 19, state: "detected", rule_id: "aws" },
          { start: 20, end: 29, state: "detected", rule_id: "aws" },
          { start: 30, end: 39, state: "retired", rule_id: "managed-secret" },
        ],
        catalog_version: "2026.07",
        screened_revision: 41,
      }),
    );
    expect(summary).toMatchObject({
      tracked: 1,
      detected: 2,
      retired: 1,
      total: 4,
      catalogVersion: "2026.07",
      screenedRevision: 41,
    });
  });
});

describe("span lookup", () => {
  const doc = Text.of(["aaaa bbbb cccc"]);
  const marks = secretSpanMarks(
    screen({
      spans: [
        { start: 0, end: 4, state: "tracked", rule_id: "managed-secret", reference: "{{paintedwolf-secret:a}}" },
        { start: 5, end: 9, state: "detected", rule_id: "aws", shape: "AAAA" },
        { start: 10, end: 14, state: "detected", rule_id: "aws", shape: "AAAA" },
      ],
    }),
    doc,
  );

  it("finds the span under the caret", () => {
    expect(secretSpanAt(marks, 2)?.state).toBe("tracked");
    expect(secretSpanAt(marks, 4)?.state).toBe("tracked");
    expect(secretSpanAt(marks, 7)?.state).toBe("detected");
  });

  it("groups siblings by evidence identity, not by position", () => {
    const siblings = siblingSecretSpans(marks, marks[1]!);
    expect(siblings).toHaveLength(1);
    expect(siblings[0]!.from).toBe(10);
  });

  it("groups tracked siblings by their capability reference", () => {
    expect(siblingSecretSpans(marks, marks[0]!)).toHaveLength(0);
  });
});
