import { describe, expect, it } from "vitest";
import { isKnownCodeOrMarkupPath } from "./pretty-classify.ts";
import { formatPrettyProjection } from "./pretty-segmenter.ts";

describe("pretty-segmenter (FMT-AC-02, FMT-AC-04)", () => {
  it("FMT-AC-04: keeps known code and markup files 1:1 passthrough including Go struct/raw literals, inline HTML, and indentation-sensitive source", () => {
    const goSource = [
      "package main",
      'const fixture = `{"a":1,"b":[2,3]}`',
      "type Config struct { Enabled bool }",
      'func main() { println(`{"log":true}`) }',
    ].join("\n");

    const goRes = formatPrettyProjection(goSource, {
      sourceIdentity: "main.go",
      sourceRevision: 1,
      path: "cmd/server/main.go",
    });
    expect(goRes.outcome).toBe("passthrough");
    expect(goRes.displayText).toBe(goSource);
    expect(goRes.sourceMap.runs).toEqual([
      {
        kind: "identity",
        dispFrom: 0,
        dispTo: goSource.length,
        srcFrom: 0,
        srcTo: goSource.length,
        insertOffset: 0,
        segmentIndex: 0,
      },
    ]);

    const htmlSource = '<div><span>{"inline":1}</span><p>text</p></div>';
    const htmlRes = formatPrettyProjection(htmlSource, {
      sourceIdentity: "index.html",
      sourceRevision: 1,
      path: "index.html",
    });
    expect(htmlRes.outcome).toBe("passthrough");
    expect(htmlRes.displayText).toBe(htmlSource);

    const pySource = [
      "def compute():",
      '    data = {"a": 1, "b": [2, 3]}',
      "    if data:",
      "        return data",
    ].join("\n");
    const pyRes = formatPrettyProjection(pySource, {
      sourceIdentity: "app.py",
      sourceRevision: 1,
      path: "app.py",
    });
    expect(pyRes.outcome).toBe("passthrough");
    expect(pyRes.displayText).toBe(pySource);

    expect(isKnownCodeOrMarkupPath("src/app.tsx")).toBe(true);
    expect(isKnownCodeOrMarkupPath("Cargo.lock")).toBe(true);
    expect(isKnownCodeOrMarkupPath("data.json")).toBe(false);
    expect(isKnownCodeOrMarkupPath("events.jsonl")).toBe(false);
  });

  it("FMT-AC-02: preserves Markdown backtick/tilde fences and only formats closed json/jsonl blocks", () => {
    const markdown = [
      "# Report",
      "```json",
      '{"status":"ok","items":[1,2]}',
      "```",
      "~~~go",
      'const raw = `{"stay":"unformatted"}`',
      "~~~",
      "```jsonl",
      '{"row":1}',
      '{"row":2}',
      "```",
      "```json",
      '{"unclosed":1}',
    ].join("\n");

    const res = formatPrettyProjection(markdown, {
      sourceIdentity: "README.md",
      sourceRevision: 3,
      path: "docs/README.md",
    });

    expect(res.outcome).toBe("formatted");
    expect(res.displayText).toBe(
      [
        "# Report",
        "```json",
        "{",
        '  "status": "ok",',
        '  "items": [',
        "    1,",
        "    2",
        "  ]",
        "}",
        "```",
        "~~~go",
        'const raw = `{"stay":"unformatted"}`',
        "~~~",
        "```jsonl",
        "{",
        '  "row": 1',
        "}",
        "{",
        '  "row": 2',
        "}",
        "```",
        "```json",
        '{"unclosed":1}',
      ].join("\n"),
    );

    const fenceSegs = res.segments.filter((s) => s.kind === "fence-delimiter");
    expect(fenceSegs).toHaveLength(6);
  });

  it("FMT-AC-02: formats complete JSON bodies inside host-framed envelopes while keeping handles and banners 1:1", () => {
    const framed = [
      "[root:worker:read#2 src/config.json:1-12]",
      "[compacted 240 bytes]",
      "verbatim head/tail:",
      '{"host":"localhost","port":8080}',
    ].join("\n");

    const res = formatPrettyProjection(framed, {
      sourceIdentity: "tool-1",
      sourceRevision: 1,
    });

    expect(res.outcome).toBe("formatted");
    expect(res.displayText).toBe(
      [
        "[root:worker:read#2 src/config.json:1-12]",
        "[compacted 240 bytes]",
        "verbatim head/tail:",
        "{",
        '  "host": "localhost",',
        '  "port": 8080',
        "}",
      ].join("\n"),
    );

    const headerSegs = res.segments.filter((s) => s.kind === "envelope-header");
    expect(headerSegs).toHaveLength(3);
  });

  it("FMT-AC-02: formats inline host markers, overlay events, and timestamped log lines", () => {
    const logText = [
      '[host:overlay-promote-event] {"promoted":true,"files":2}',
      '2026-11-09T13:47:08Z INFO [api] {"req":"abc","ms":12}',
      "Rejected: invalid argument",
      "Code: invalid_request",
      '{"detail":"missing field"}',
    ].join("\n");

    const res = formatPrettyProjection(logText, {
      sourceIdentity: "logs",
      sourceRevision: 1,
    });

    expect(res.outcome).toBe("formatted");
    expect(res.displayText).toBe(
      [
        "[host:overlay-promote-event] {",
        '  "promoted": true,',
        '  "files": 2',
        "}",
        "2026-11-09T13:47:08Z INFO [api] {",
        '  "req": "abc",',
        '  "ms": 12',
        "}",
        "Rejected: invalid argument",
        "Code: invalid_request",
        "{",
        '  "detail": "missing field"',
        "}",
      ].join("\n"),
    );
  });

  it("FMT-AC-02: never joins truncated excerpts across compaction banners or ellipsis lines", () => {
    const compactedExcerpts = [
      "[read#1 large.json:1-100]",
      "[compacted 8192 bytes]",
      "verbatim head/tail:",
      '{"items": [1, 2,',
      "[compacted 4096 bytes]",
      "99, 100]}",
    ].join("\n");

    const res = formatPrettyProjection(compactedExcerpts, {
      sourceIdentity: "compacted-tool",
      sourceRevision: 1,
    });

    expect(res.outcome).toBe("passthrough");
    expect(res.displayText).toBe(compactedExcerpts);
  });
});
