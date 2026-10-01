// @vitest-environment jsdom
import { EditorState, StateEffect, StateField } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import { agentDocumentMarks } from "../../../files/components/agent-presence.ts";
import type { PaintedAgentMark } from "../../../files/documents/document-agent-presence.ts";
import { lineFactsGutter, lineFactsMarker, lineFactsTint } from "./line-facts-gutter.ts";

let view: EditorView;
afterEach(() => { view?.destroy(); document.body.replaceChildren(); vi.restoreAllMocks(); });

function mount(withRead: boolean) {
  const parent = document.body.appendChild(document.createElement("div"));
  const doc = EditorState.create({ doc: "one\ntwo\nthree\nfour" }).doc;
  const marks: PaintedAgentMark[] = withRead ? agentDocumentMarks(new Map([["reader", {
    session_id: "reader", title: "Read", turn: 1, activities: [], intents: [], worker_drafts: [],
    reads: [{ id: "read", sequence: 1, tool_call_id: "call", tool: "read", root_id: "root", path: "a.ts",
      extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false }],
  }]]), { reads: true, changes: true, fileNames: true }, "reader", "root", "a.ts").map(mark => ({
    mark, from: doc.line(2).from, to: doc.line(4).from, bornAt: 0, leftAt: null, color: undefined,
  })) : [];
  const setMarks = StateEffect.define<PaintedAgentMark[]>();
  const markField = StateField.define<PaintedAgentMark[]>({ create: () => marks,
    update: (value, transaction) => {
      for (const effect of transaction.effects) if (effect.is(setMarks)) return effect.value;
      return value;
    } });
  const attribution: [] = [], findings: [] = [];
  view = new EditorView({ parent, state: EditorState.create({ doc, extensions: [markField, lineFactsGutter(state => ({
    doc: state.doc, marks: state.field(markField), attribution, findings,
  }))] }) });
  const buttons = [1, 2, 3, 4].map(line => {
    const button = view.dom.appendChild(document.createElement("button"));
    button.className = "files-line-gutter__facts";
    button.dataset.line = String(line);
    return button;
  });
  return { marks, setMarks, buttons, lines: [...view.contentDOM.querySelectorAll(".cm-line")] };
}
function move(line: Element) { line.dispatchEvent(new MouseEvent("mousemove", { bubbles: true })); }

describe("read gutter facts", () => {
  it("does no coordinate or DOM-position work on pointer movement", () => {
    const { lines } = mount(true);
    const coords = vi.spyOn(view, "posAtCoords");
    const position = vi.spyOn(view, "posAtDOM");
    for (const line of lines) move(line);
    expect(coords).not.toHaveBeenCalled();
    expect(position).not.toHaveBeenCalled();
  });

  it("does not apply mouseover or is-hot classes to read gutter handles on pointer movement", () => {
    const { lines, buttons } = mount(true);
    for (const line of lines) move(line);
    expect(buttons.every(button => !button.classList.contains("is-hot"))).toBe(true);
  });

  it("marks read gutter handles with read classification", () => {
    const { marks } = mount(true);
    const doc = EditorState.create({ doc: "one\ntwo\nthree\nfour" }).doc;
    const input = { doc, marks, attribution: [], findings: [] };
    const markerLine2 = lineFactsMarker(input, 2);
    expect(markerLine2?.hasRead).toBe(true);
    expect(markerLine2?.elementClass).toContain("files-line-gutter__cell--read");
    const dom = markerLine2?.toDOM();
    expect(dom?.classList.contains("files-line-gutter__facts--read")).toBe(true);
    expect(dom?.dataset.read).toBe("true");

    const markerLine1 = lineFactsMarker(input, 1);
    expect(markerLine1).toBeNull();
  });

  it("provides a single uniform tint across overlapping reads without stacking gradients", () => {
    const doc = EditorState.create({ doc: "one\ntwo\nthree\nfour" }).doc;
    const marks: PaintedAgentMark[] = [
      ...agentDocumentMarks(new Map([["reader1", {
        session_id: "reader1", title: "Read 1", turn: 1, activities: [], intents: [], worker_drafts: [],
        reads: [{ id: "read1", sequence: 1, tool_call_id: "call1", tool: "read", root_id: "root", path: "a.ts",
          extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false }],
      }]]), { reads: true, changes: true, fileNames: true }, "reader1", "root", "a.ts").map(mark => ({
        mark, from: doc.line(2).from, to: doc.line(4).from, bornAt: 0, leftAt: null, color: undefined,
      })),
      ...agentDocumentMarks(new Map([["reader2", {
        session_id: "reader2", title: "Read 2", turn: 1, activities: [], intents: [], worker_drafts: [],
        reads: [{ id: "read2", sequence: 2, tool_call_id: "call2", tool: "read", root_id: "root", path: "a.ts",
          extent: "range", ranges: [{ start_line: 2, end_line: 4 }], stale: false }],
      }]]), { reads: true, changes: true, fileNames: true }, "reader2", "root", "a.ts").map(mark => ({
        mark, from: doc.line(2).from, to: doc.line(4).to, bornAt: 0, leftAt: null, color: undefined,
      })),
    ];
    const input = { doc, marks, attribution: [], findings: [] };
    const tintLine2 = lineFactsTint(input, 2);
    // Should be a single linear-gradient without commas separating stacked gradients
    expect(tintLine2.startsWith("linear-gradient")).toBe(true);
    expect(tintLine2.includes("), linear-gradient")).toBe(false);

    const tintLine3 = lineFactsTint(input, 3);
    expect(tintLine3).toBe(tintLine2);
  });

  it("suppresses range read gradients when a whole-file read is already active", () => {
    const doc = EditorState.create({ doc: "one\ntwo\nthree\nfour" }).doc;
    const marks: PaintedAgentMark[] = agentDocumentMarks(new Map([["reader", {
      session_id: "reader", title: "Read", turn: 1, activities: [], intents: [], worker_drafts: [],
      reads: [
        { id: "whole", sequence: 1, tool_call_id: "whole", tool: "read", root_id: "root", path: "a.ts",
          extent: "whole_file", ranges: [], stale: false },
        { id: "range", sequence: 2, tool_call_id: "range", tool: "read", root_id: "root", path: "a.ts",
          extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false },
      ],
    }]]), { reads: true, changes: true, fileNames: true }, "reader", "root", "a.ts").map(mark => ({
      mark, from: 0, to: doc.length, bornAt: 0, leftAt: null, color: undefined,
    }));
    const input = { doc, marks, attribution: [], findings: [] };
    expect(lineFactsTint(input, 2)).toBe("none");

    const marker = lineFactsMarker(input, 2);
    expect(marker?.hasRead).toBe(true);
    expect(marker?.tint).toBe("none");
  });
});
