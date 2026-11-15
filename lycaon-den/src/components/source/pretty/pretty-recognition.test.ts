import { EditorState, Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { createPrettyExtension, prettyOutcomeOf } from "./pretty-layout.ts";
import { prettyPlanFor, prettyTargetFor, recognizeJsonContainers } from "./pretty-recognition.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function decorationCount(state: EditorState): number {
  let count = 0;
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, () => {
    count += 1;
  });
  return count;
}

function prng(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 0x100000000;
  };
}

describe("grammatical container recognition and active-with-layout outcome", () => {
  it("activates on .log, .out, and .txt with containers, and reports unavailable on plain prose .txt", () => {
    const logState = EditorState.create({
      doc: '2026-11-14 INFO {"user":"a","roles":[1,2]}\n',
      extensions: [createPrettyExtension({ enabled: true, path: "server.log" })],
    });
    expect(prettyOutcomeOf(logState).kind).toBe("active");
    expect(decorationCount(logState)).toBeGreaterThan(0);

    const outState = EditorState.create({
      doc: "trace: {'ok': True, 'items': [1, 2]}\n",
      extensions: [createPrettyExtension({ enabled: true, path: "trace.out" })],
    });
    expect(prettyOutcomeOf(outState).kind).toBe("active");

    const txtJsonState = EditorState.create({
      doc: '{"a":1,"b":[2,3]}\n',
      extensions: [createPrettyExtension({ enabled: true, path: "notes.txt" })],
    });
    expect(prettyOutcomeOf(txtJsonState).kind).toBe("active");

    const txtProseState = EditorState.create({
      doc: "Just plain prose without any JSON containers.\n",
      extensions: [createPrettyExtension({ enabled: true, path: "notes.txt" })],
    });
    expect(prettyOutcomeOf(txtProseState).kind).toBe("unavailable");
    expect(decorationCount(txtProseState)).toBe(0);
  });

  it("extracts multiple containers on an anchored log line and skips trailing metadata", () => {
    const line =
      '2026-11-14T08:00:00Z INFO req={"user":"a","roles":[1,2]} resp={"ok":true} latency=4ms';
    const spans = recognizeJsonContainers(Text.of([line]));
    expect(spans).toHaveLength(2);
    expect(line.slice(spans[0]!.from, spans[0]!.to)).toBe('{"user":"a","roles":[1,2]}');
    expect(line.slice(spans[1]!.from, spans[1]!.to)).toBe('{"ok":true}');

    const trailingBracket = '2026-11-14 INFO {"a":1} [ms=12]';
    const oneSpan = recognizeJsonContainers(Text.of([trailingBracket]));
    expect(oneSpan).toHaveLength(1);
    expect(trailingBracket.slice(oneSpan[0]!.from, oneSpan[0]!.to)).toBe('{"a":1}');
  });

  it("rejects non-grammatical containers and stops on truncated multi-line JSON at EOF", () => {
    for (const bad of [
      '{"a": 1,}',
      "[1, 2,]",
      '{"a" 1}',
      '{a: 1}',
      "[1 2]",
      '{"a": 1 "b": 2}',
      '{"a": }',
      "[, 1]",
    ]) {
      expect(recognizeJsonContainers(Text.of([bad])), bad).toEqual([]);
    }

    const truncatedAtEof = '[\n  {"id": 1, "tags": ["a", "b"]},\n  {"id": 2, "tags": ["c"';
    expect(recognizeJsonContainers(Text.of(truncatedAtEof.split("\n")))).toEqual([]);
  });

  it("recognises every valid JSONL row regardless of preceding broken lines", () => {
    const brokenPool = [
      "2026-11-14T10:00:00Z INFO truncated: {",
      "2026-11-14T10:00:01Z WARN bad array: [1, 2,",
      '{"unterminated": "value without quote',
      '{"trailing": 1,}',
      "[10, 20,]",
      '{"missing_colon" 42}',
      "[1 2 3]",
      "plain prose with { braces } and [ brackets ]",
    ];
    const validRows = [
      '{"id":0,"ok":true,"tags":["alpha","beta"]}',
      '2026-11-14T10:00:02Z INFO event {"id":1,"nested":{"count":3}}',
      '[1,{"id":2,"flag":false},null]',
      "trace: {'id': 3, 'ok': True, 'empty': None}",
      '[worker-1] [rpc] {"id":4,"items":[10,20]}',
    ];

    for (let seed = 1; seed <= 120; seed++) {
      const rand = prng(seed * 977);
      const lines: string[] = [];
      const expectedSlices: string[] = [];
      for (let i = 0; i < 12; i++) {
        if (rand() < 0.65) {
          lines.push(brokenPool[Math.floor(rand() * brokenPool.length)]!);
        }
        const row = validRows[Math.floor(rand() * validRows.length)]!;
        lines.push(row);
        const rowSpans = recognizeJsonContainers(Text.of([row]));
        expect(rowSpans.length).toBe(1);
        expectedSlices.push(row.slice(rowSpans[0]!.from, rowSpans[0]!.to));
      }
      // Ensure the last line is a complete valid row so the document does not end in a grammatical EOF prefix.
      const docText = lines.join("\n") + "\n";
      const doc = Text.of(docText.split("\n"));
      const actual = recognizeJsonContainers(doc).map((s) => doc.sliceString(s.from, s.to));
      expect(actual, `seed ${seed}`).toEqual(expectedSlices);
    }
  });

  it("keeps NO_LAYOUT formats unavailable with zero spans", () => {
    for (const path of ["changes.diff", "unit.pas", "vars.env", "build.cmake"]) {
      const target = prettyTargetFor(path);
      expect(target, path).toBeNull();
      const state = EditorState.create({
        doc: '{"a": 1}\n',
        extensions: [createPrettyExtension({ enabled: true, path })],
      });
      expect(prettyOutcomeOf(state).kind).toBe("unavailable");
      expect(decorationCount(state)).toBe(0);
    }
    const plan = prettyPlanFor(Text.of(['{"a":1}']), prettyTargetFor("notes.txt")!);
    expect(plan.kind).toBe("spans");
  });
});
