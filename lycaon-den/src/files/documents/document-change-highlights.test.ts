// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EditorState, type ChangeSpec } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { AGENT_LINGER_MS } from "../components/agent-presence.ts";
import { documentRemoteChange } from "./document-editor-binding.ts";
import {
  LANDED_LINES_MAX,
  LANDED_LINE_FADE_MS,
  documentChangeHighlights,
  landedLines,
} from "./document-change-highlights.ts";

const text = (lines: number) => Array.from({ length: lines }, (_, index) => `line ${index + 1}`).join("\n");

function remote(state: EditorState, changes: ChangeSpec): EditorState {
  return state.update({ changes, annotations: documentRemoteChange.of(true) }).state;
}

function local(state: EditorState, changes: ChangeSpec): EditorState {
  return state.update({ changes }).state;
}

/** Replaces line `number` with new text. */
function rewrite(state: EditorState, number: number): ChangeSpec {
  const line = state.doc.line(number);
  return { from: line.from, to: line.to, insert: `changed ${number}` };
}

function tinted(state: EditorState): number[] {
  return landedLines(state).map((line) => state.doc.lineAt(line.from).number).sort((a, b) => a - b);
}

describe("landed lines", () => {
  beforeEach(() => {
    vi.useFakeTimers({ now: 1_000 });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("keeps every line a burst of changes landed on", () => {
    let state = EditorState.create({ doc: text(20), extensions: documentChangeHighlights });
    state = remote(state, rewrite(state, 3));
    state = remote(state, rewrite(state, 12));
    expect(tinted(state)).toEqual([3, 12]);
  });

  it("records lines anywhere in the document", () => {
    let state = EditorState.create({ doc: text(2_000), extensions: documentChangeHighlights });
    state = remote(state, rewrite(state, 1_900));
    expect(tinted(state)).toEqual([1_900]);
  });

  it("restarts a line's time when another change lands on it", () => {
    let state = EditorState.create({ doc: text(10), extensions: documentChangeHighlights });
    state = remote(state, rewrite(state, 4));
    vi.advanceTimersByTime(5_000);
    state = remote(state, rewrite(state, 4));
    expect(landedLines(state)).toEqual([expect.objectContaining({ at: 6_000 })]);
  });

  it("follows its lines as text changes above them", () => {
    let state = EditorState.create({ doc: text(10), extensions: documentChangeHighlights });
    state = remote(state, rewrite(state, 5));
    state = local(state, { from: 0, insert: "new\nlines\n" });
    expect(tinted(state)).toEqual([7]);
    state = local(state, { from: 0, to: state.doc.line(3).from });
    expect(tinted(state)).toEqual([5]);
  });

  it("drops a line whose start was deleted", () => {
    let state = EditorState.create({ doc: text(10), extensions: documentChangeHighlights });
    state = remote(state, rewrite(state, 5));
    state = remote(state, { from: state.doc.line(4).to, to: state.doc.line(5).to });
    expect(tinted(state)).toEqual([4]);
  });

  it("clears only the lines the person edits", () => {
    let state = EditorState.create({ doc: text(10), extensions: documentChangeHighlights });
    state = remote(state, [rewrite(state, 2), rewrite(state, 6)]);
    state = local(state, { from: state.doc.line(6).from, insert: "x" });
    expect(tinted(state)).toEqual([2]);
  });

  it("keeps the newest lines when a burst exceeds the cap", () => {
    let state = EditorState.create({ doc: text(700), extensions: documentChangeHighlights });
    state = remote(state, Array.from({ length: 300 }, (_, index) => rewrite(state, index + 1)));
    vi.advanceTimersByTime(10);
    state = remote(state, Array.from({ length: 300 }, (_, index) => rewrite(state, index + 301)));
    const lines = tinted(state);
    expect(lines).toHaveLength(LANDED_LINES_MAX);
    expect(lines.slice(-300)).toEqual(Array.from({ length: 300 }, (_, index) => index + 301));
    expect(lines.filter((line) => line <= 300)).toHaveLength(LANDED_LINES_MAX - 300);
  });
});

describe("landed line expiry", () => {
  let view: EditorView | undefined;
  beforeEach(() => {
    vi.useFakeTimers({ now: 1_000 });
  });
  afterEach(() => {
    view?.destroy();
    view = undefined;
    vi.useRealTimers();
  });

  const classes = () => [...view!.contentDOM.querySelectorAll(".cm-line")].map((line) => line.className);

  it("tints at once, starts leaving at the linger time, and goes after the fade", () => {
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: text(5), extensions: documentChangeHighlights }) });
    view.dispatch({ changes: rewrite(view.state, 2), annotations: documentRemoteChange.of(true) });
    vi.advanceTimersByTime(3_000);
    view.dispatch({ changes: rewrite(view.state, 4), annotations: documentRemoteChange.of(true) });
    expect(classes()[1]).toContain("cm-den-hunk-add");
    expect(classes()[1]).not.toContain("cm-den-hunk--leaving");

    vi.advanceTimersByTime(AGENT_LINGER_MS - 3_000);
    expect(classes()[1]).toContain("cm-den-hunk--leaving");
    expect(classes()[3]).not.toContain("cm-den-hunk--leaving");

    vi.advanceTimersByTime(LANDED_LINE_FADE_MS);
    expect(classes()[1]).not.toContain("cm-den-hunk");
    expect(classes()[3]).toContain("cm-den-hunk-add");

    vi.advanceTimersByTime(3_000);
    expect(landedLines(view.state)).toEqual([]);
    expect(classes().some((name) => name.includes("cm-den-hunk"))).toBe(false);
  });
});
