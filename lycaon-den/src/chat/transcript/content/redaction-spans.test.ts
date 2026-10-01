import { describe, expect, it } from "vitest";

import {
  allSpans,
  redactionMarkModifier,
  redactionMarkTitle,
  redactionSummary,
  segmentBySpans,
  spansForField,
} from "./redaction-spans.ts";
import type { Message, RedactedSpan } from "../../../api/types.ts";

function secretSpan(over: Partial<RedactedSpan> = {}): RedactedSpan {
  return {
    field: "content",
    start: 0,
    length: 10,
    kind: "secret",
    source: "shape_rule",
    rule_id: "kingfisher.gitea.1",
    rule_title: "Gitea Access Token",
    ...over,
  };
}

function messageWith(spans: RedactedSpan[]): Pick<Message, "host_secret_redaction"> {
  return { host_secret_redaction: { spans } };
}

describe("redaction spans", () => {
  it("marks only where the host says, not where the marker text appears", () => {
    // The literal marker occurs twice; only one is a real redaction.
    const text = "real [REDACTED] and quoted [REDACTED]";
    const message = messageWith([secretSpan({ start: 5, length: 10 })]);
    const segments = segmentBySpans(text, spansForField(message, "content"));
    const marks = segments.filter((s) => s.kind === "mark");
    expect(marks).toHaveLength(1);
    expect(segments.map((s) => s.text).join("")).toBe(text);
  });

  it("renders nothing as a mark when the host recorded no spans", () => {
    const segments = segmentBySpans("[REDACTED] is just text here", []);
    expect(segments).toEqual([{ kind: "text", text: "[REDACTED] is just text here" }]);
  });

  it("splits on code points so a span after an emoji stays aligned", () => {
    const text = "🔒 [REDACTED] tail";
    const message = messageWith([secretSpan({ start: 2, length: 10 })]);
    const segments = segmentBySpans(text, spansForField(message, "content"));
    const mark = segments.find((s) => s.kind === "mark");
    expect(mark?.text).toBe("[REDACTED]");
    expect(segments.map((s) => s.text).join("")).toBe(text);
  });

  it("keeps fields apart", () => {
    const message = messageWith([
      secretSpan({ field: "content", start: 0 }),
      secretSpan({ field: "tool_result.content", start: 4 }),
    ]);
    expect(spansForField(message, "content")).toHaveLength(1);
    expect(spansForField(message, "tool_result.content")[0]?.start).toBe(4);
    expect(allSpans(message)).toHaveLength(2);
  });

  it("summarises secrets and masks separately", () => {
    const message = messageWith([
      secretSpan(),
      secretSpan({ start: 20, kind: "observer_mask", source: "policy", rule_title: undefined }),
    ]);
    const summary = redactionSummary(message);
    expect(summary).toEqual({ secrets: 1, references: 0, masked: 1, titles: ["Gitea Access Token"] });
  });

  it("counts a protected value written as its reference apart from a removal", () => {
    const message = messageWith([
      secretSpan(),
      secretSpan({
        start: 20,
        length: 58,
        kind: "managed_reference",
        source: "remembered_match",
        rule_id: "managed-secret",
        rule_title: "A managed secret",
      }),
    ]);
    expect(redactionSummary(message)).toEqual({
      secrets: 1,
      references: 1,
      masked: 0,
      titles: ["Gitea Access Token", "A managed secret"],
    });
  });

  it("gives each kind of mark its own claim", () => {
    const reference = secretSpan({ kind: "managed_reference", source: "remembered_match" });
    expect(redactionMarkModifier(reference)).toBe("den-redaction-mark--reference");
    expect(redactionMarkTitle(reference)).toContain("the source still holds the value");
    expect(redactionMarkModifier(secretSpan({ kind: "observer_mask" }))).toBe("den-redaction-mark--mask");
    expect(redactionMarkModifier(secretSpan())).toBeUndefined();
    expect(redactionMarkTitle(secretSpan())).toContain("Gitea Access Token");
  });

  it("has no summary when nothing was replaced", () => {
    expect(redactionSummary({ host_secret_redaction: undefined })).toBeUndefined();
    expect(redactionSummary(undefined)).toBeUndefined();
  });
});
