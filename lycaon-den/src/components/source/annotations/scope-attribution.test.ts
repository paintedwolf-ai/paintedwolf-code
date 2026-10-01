// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { EditorState, Transaction } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import type { SourceAttributedText, SourceContributor } from "../../../api/types.ts";
import { applyEditorScopeDiff, editorScopeDiffChunks, editorScopeDiffOriginal, scopeDiffExtension } from "../diff/scope-diff.ts";
import { buildAttributedChanges } from "./scope-attribution.ts";
import { TEST_OWNER_PERSON_ID } from "../../../platform/connection/host-identity-test.ts";

const author = (session: string, origin: SourceContributor["origin"] = "agent"): SourceContributor => ({
  origin, session_id: session, turn: 1, person_id: origin === "user" ? TEST_OWNER_PERSON_ID : "", actor_label: session,
  tool_call_id: "", tool_name: "edit", worker_id: "",
});
const run = (index: number, length: number, session: string, selected: boolean, visible = true): SourceAttributedText => ({
  index, length, contributors: [author(session, visible ? "agent" : "user")], selected, visible,
});
let view: EditorView | undefined;
afterEach(() => { view?.destroy(); view = undefined; document.body.replaceChildren(); });

describe("chat comparison authorship", () => {
  it("marks the selected chat, labels other chats, and leaves human typing unmarked", () => {
    const text = "first typed\nbase\nsecond 🐺\n";
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: text, extensions: scopeDiffExtension }) });
    applyEditorScopeDiff(view, { original: "base\n", tip: text, attribution: { before: [], after: [
      run(0, 5, "Fix auth", true), run(5, 6, "person", false, false),
      run(11, 1, "Fix auth", true), run(17, 10, "Add tests", false),
    ] } });
    expect(editorScopeDiffOriginal(view.state)).toBe("base\n");
    const primary = [...view.contentDOM.querySelectorAll(".cm-changedText")].map((el) => el.textContent).join("");
    expect(primary).toBe("first");
    const context = view.contentDOM.querySelector(".cm-den-contextText");
    expect(context?.textContent).toBe("second 🐺");
    expect(context?.getAttribute("data-tip")).toContain("Add tests");
    expect(primary).not.toContain("typed");
    expect(view.state.doc.toString()).toBe(text);
  });

  it("does not extend another author's mark over newly typed characters", () => {
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "first", extensions: scopeDiffExtension }) });
    applyEditorScopeDiff(view, { original: "", tip: "first", attribution: { before: [], after: [run(0, 5, "Fix auth", true)] } });
    view.dispatch({ changes: { from: 2, insert: "human" }, annotations: Transaction.userEvent.of("input.type") });
    const marks = [...view.contentDOM.querySelectorAll(".cm-changedText")].map((el) => el.textContent);
    expect(marks).toEqual(["fi", "rst"]);
    expect(view.state.doc.toString()).toBe("fihumanrst");
  });

  it("labels deletions from another chat without marking them as the selected chat", () => {
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "base\n", extensions: scopeDiffExtension }) });
    applyEditorScopeDiff(view, { original: "other\nbase\n", tip: "base\n", attribution: { before: [run(0, 6, "Remove legacy", false)], after: [] } });
    const deletion = view.contentDOM.querySelector(".cm-den-contextDeletion");
    expect(deletion?.textContent).toBe("other");
    expect(deletion?.getAttribute("data-tip")).toContain("Remove legacy");
  });
});


describe("visible authorship decorations", () => {
  it("bounds whole-file attribution to visible ranges, including folded gaps", () => {
    view = new EditorView({ state: EditorState.create({ doc: Array.from({ length: 10000 }, (_, i) => `line ${i}`).join("\n") }) });
    const doc = view.state.doc;
    const ranges = [{ from: doc.line(500).from, to: doc.line(505).to },
      { from: doc.line(9000).from, to: doc.line(9002).to }];
    Object.defineProperty(view, "visibleRanges", { value: ranges });
    const decorations = buildAttributedChanges(view, [run(0, doc.length, "Whole file", true)]);
    const lines: number[] = [], spans: { from: number; to: number }[] = [];
    for (let cursor = decorations.iter(); cursor.value; cursor.next()) {
      if (cursor.from === cursor.to) lines.push(doc.lineAt(cursor.from).number);
      else spans.push({ from: cursor.from, to: cursor.to });
    }
    expect(lines).toEqual([500, 501, 502, 503, 504, 505, 9000, 9001, 9002]);
    expect(spans).toEqual(ranges);
  });
});


describe("comparison refresh", () => {
  it("retains the comparison and DOM across equivalent responses, including after typing", () => {
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "new\n", extensions: scopeDiffExtension }) });
    const input = { original: "old\n", tip: "new\n", attribution: {
      before: [run(0, 4, "Change", true)], after: [run(0, 4, "Change", true)],
    } };
    applyEditorScopeDiff(view, input);
    view.dispatch({ changes: { from: 0, insert: "typed " } });
    const chunks = editorScopeDiffChunks(view.state);
    const removed = view.contentDOM.querySelector(".cm-deletedChunk");
    const dispatch = vi.spyOn(view, "dispatch");
    for (let i = 0; i < 100; i++) applyEditorScopeDiff(view, structuredClone(input));
    expect(dispatch).not.toHaveBeenCalled();
    expect(editorScopeDiffChunks(view.state)).toBe(chunks);
    expect(view.contentDOM.querySelector(".cm-deletedChunk")).toBe(removed);
    expect(view.state.doc.toString()).toBe("typed new\n");
  });

  it("reuses the text diff while updating deletion authorship and selection", () => {
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "new\n", extensions: scopeDiffExtension }) });
    applyEditorScopeDiff(view, { original: "old\n", attribution: {
      before: [run(0, 4, "First", true)], after: [run(0, 4, "First", true)],
    } });
    const chunks = editorScopeDiffChunks(view.state);
    applyEditorScopeDiff(view, { original: "old\n", attribution: {
      before: [run(0, 4, "Second", false)], after: [run(0, 4, "Second", false)],
    } });
    expect(editorScopeDiffChunks(view.state)).toBe(chunks);
    expect(view.contentDOM.querySelector(".cm-den-contextDeletion")?.getAttribute("data-tip")).toContain("Second");
    expect(view.contentDOM.querySelector(".cm-den-contextText")?.getAttribute("data-tip")).toContain("Second");
  });
});
