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

describe("CONTAINERS fallback and log recognition", () => {
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
