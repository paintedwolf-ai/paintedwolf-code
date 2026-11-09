import { describe, expect, it } from "vitest";
import { formatPrettyProjection } from "./pretty-segmenter.ts";

describe("pretty-printer (FMT-AC-01, FMT-AC-03)", () => {
  it("FMT-AC-01: preserves exact numeric lexemes, duplicate keys, numeric key order, escapes, and literal string spaces", () => {
    const raw =
      '{"10":1.20e+3,"2":-0.00,"dup":"first  with   spaces","dup":"second\\u003c\\n\\\"","big":9007199254740993,"tiny":1e-005}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "file:///test.json",
      sourceRevision: 1,
      path: "test.json",
    });

    expect(res.outcome).toBe("formatted");
    expect(res.displayText).toBe(
      [
        "{",
        '  "10": 1.20e+3,',
        '  "2": -0.00,',
        '  "dup": "first  with   spaces",',
        '  "dup": "second\\u003c\\n\\\"",',
        '  "big": 9007199254740993,',
        '  "tiny": 1e-005',
        "}",
      ].join("\n"),
    );

    // Every identity run must match its exact source substring 1:1.
    const identityRuns = res.sourceMap.runs.filter((r) => r.kind === "identity");
    const reconstructedTokens = identityRuns
      .map((r) => res.displayText.slice(r.dispFrom, r.dispTo))
      .join("");
    expect(reconstructedTokens).toBe(raw);
    for (const run of identityRuns) {
      expect(res.displayText.slice(run.dispFrom, run.dispTo)).toBe(
        raw.slice(run.srcFrom, run.srcTo),
      );
    }

    // Leading-zero numeric lexeme (01.20e+3) is invalid strict RFC 8259 JSON and falls back to raw passthrough.
    const leadingZeroRaw = '{"10":01.20e+3}';
    const leadingZeroRes = formatPrettyProjection(leadingZeroRaw, {
      sourceIdentity: "file:///invalid-leading-zero.json",
      sourceRevision: 1,
      path: "invalid-leading-zero.json",
    });
    expect(leadingZeroRes.outcome).toBe("passthrough");
    expect(leadingZeroRes.displayText).toBe(leadingZeroRaw);
    expect(leadingZeroRes.sourceMap.runs[0]?.kind).toBe("raw-fallback");
  });

  it("FMT-AC-01: keeps empty objects and arrays compact while recording removed interior whitespace", () => {
    const raw = '{"emptyObj":{   },"emptyArr":[ \t ],"nested":[{},[]]}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "doc-empty",
      sourceRevision: 2,
    });

    expect(res.outcome).toBe("formatted");
    expect(res.displayText).toBe(
      [
        "{",
        '  "emptyObj": {},',
        '  "emptyArr": [],',
        '  "nested": [',
        "    {},",
        "    []",
        "  ]",
        "}",
      ].join("\n"),
    );

    const zeroDispVirtual = res.sourceMap.runs.filter(
      (r) => r.kind === "virtual-layout" && r.dispFrom === r.dispTo && r.srcTo > r.srcFrom,
    );
    expect(zeroDispVirtual).toHaveLength(2);
    expect(zeroDispVirtual[0]?.srcFrom).toBe(13);
    expect(zeroDispVirtual[0]?.srcTo).toBe(16);
  });

  it("FMT-AC-03: never decodes or unescapes JSON strings containing escaped JSON", () => {
    const raw =
      '{"outer":"{\\"nested\\": 1, \\"arr\\": [true, false]}","escaped":"line1\\nline2\\t\\u003cscript\\u003e"}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "doc-escaped",
      sourceRevision: 1,
      path: "payload.json",
    });

    expect(res.outcome).toBe("formatted");
    expect(res.displayText).toBe(
      [
        "{",
        '  "outer": "{\\"nested\\": 1, \\"arr\\": [true, false]}",',
        '  "escaped": "line1\\nline2\\t\\u003cscript\\u003e"',
        "}",
      ].join("\n"),
    );
    // No Apply candidate or serialization field exists on the projection result.
    expect("applyCandidate" in res).toBe(false);
    expect("serializedText" in res).toBe(false);
  });

  it("FMT-AC-03: expands JSONL records independently and preserves LF and CRLF record-delimiter runs", () => {
    const raw = '{"id":1,"name":"alpha"}\r\n{"id":2,"tags":["x","y"]}\n{"id":3,"ok":true}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "stream.jsonl",
      sourceRevision: 4,
      path: "stream.jsonl",
    });

    expect(res.outcome).toBe("formatted");
    expect(res.segments.map((s) => s.kind)).toEqual([
      "jsonl-record",
      "jsonl-record",
      "jsonl-record",
    ]);

    const delims = res.sourceMap.runs.filter((r) => r.kind === "record-delimiter");
    expect(delims).toHaveLength(2);
    expect(raw.slice(delims[0]!.srcFrom, delims[0]!.srcTo)).toBe("\r\n");
    expect(res.displayText.slice(delims[0]!.dispFrom, delims[0]!.dispTo)).toBe("\r\n");
    expect(raw.slice(delims[1]!.srcFrom, delims[1]!.srcTo)).toBe("\n");
    expect(res.displayText.slice(delims[1]!.dispFrom, delims[1]!.dispTo)).toBe("\n");
  });

  it("FMT-AC-03: keeps malformed JSONL lines as raw-fallback while neighbor records still expand", () => {
    const raw = '{"valid":1}\n{"broken": [1, 2\n{"valid":2,"extra":false}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "partial.jsonl",
      sourceRevision: 5,
      path: "partial.jsonl",
    });

    expect(res.outcome).toBe("formatted");
    expect(res.segments).toHaveLength(3);
    expect(res.segments[0]?.formatted).toBe(true);
    expect(res.segments[1]?.formatted).toBe(false);
    expect(res.segments[1]?.kind).toBe("raw");
    expect(res.segments[2]?.formatted).toBe(true);

    const fallbackRuns = res.sourceMap.runs.filter((r) => r.kind === "raw-fallback");
    expect(fallbackRuns).toHaveLength(1);
    expect(res.displayText.slice(fallbackRuns[0]!.dispFrom, fallbackRuns[0]!.dispTo)).toBe(
      '{"broken": [1, 2',
    );
    expect(res.displayText).toBe(
      [
        "{",
        '  "valid": 1',
        "}",
        '{"broken": [1, 2',
        "{",
        '  "valid": 2,',
        '  "extra": false',
        "}",
      ].join("\n"),
    );
  });

  it("FMT-AC-03: honors active rawSegmentRanges on a single JSONL record without unformatting neighbors", () => {
    const raw = '{"r":1}\n{"r":2}\n{"r":3}';
    const res = formatPrettyProjection(raw, {
      sourceIdentity: "active-edit.jsonl",
      sourceRevision: 6,
      path: "active-edit.jsonl",
      rawSegmentRanges: [{ srcFrom: 8, srcTo: 15 }],
    });

    expect(res.outcome).toBe("formatted");
    expect(res.segments[0]?.formatted).toBe(true);
    expect(res.segments[1]?.formatted).toBe(false);
    expect(res.segments[2]?.formatted).toBe(true);
    expect(res.displayText).toBe(
      ["{", '  "r": 1', "}", '{"r":2}', "{", '  "r": 3', "}"].join("\n"),
    );
  });
});
