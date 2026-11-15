// @vitest-environment jsdom
import { defaultKeymap } from "@codemirror/commands";
import { EditorSelection, EditorState, StateEffect, StateField } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AgentSessionPresence, SecurityFinding } from "../../../api/types.ts";
import { agentDocumentMarks } from "../../../files/components/agent-presence.ts";
import type { PaintedAgentMark } from "../../../files/documents/agent-paint-lifetime.ts";
import { lineFacts, lineFactsHint, lineFactsHintLabel, type LineFactsInput } from "./line-facts.ts";
import { lineFactsGutter, lineFactsMarker, lineFactsTint, setLineFactsHandlers } from "./line-facts-gutter.ts";

let view: EditorView | undefined;
afterEach(() => { view?.destroy(); view = undefined; document.body.replaceChildren(); vi.restoreAllMocks(); });

const baseDoc = EditorState.create({ doc: "one\ntwo\nthree\nfour" }).doc;

function paintedFrom(fields: Partial<AgentSessionPresence>, id = "chat"): PaintedAgentMark[] {
  const presence: AgentSessionPresence = {
    session_id: id, title: `Chat ${id}`, turn: 1, activities: [], reads: [], intents: [], worker_drafts: [], ...fields,
  };
  return agentDocumentMarks(new Map([[id, presence]]), { reads: true, changes: true, fileNames: true }, id, "root", "a.ts")
    .map(mark => ({
      mark,
      from: mark.range ? baseDoc.line(mark.range.start_line).from : 0,
      to: mark.range ? baseDoc.line(mark.range.end_line).to : baseDoc.length,
      bornAt: 0,
      leftAt: null,
      supersededAt: null,
      color: undefined,
    }));
}

function mountPlugin() {
  const parent = document.body.appendChild(document.createElement("div"));
  const setMarks = StateEffect.define<PaintedAgentMark[]>();
  const markField = StateField.define<PaintedAgentMark[]>({
    create: () => [],
    update: (value, tr) => {
      for (const effect of tr.effects) if (effect.is(setMarks)) return effect.value;
      return value;
    },
  });
  const attribution: [] = [], findings: [] = [];
  view = new EditorView({
    parent,
    state: EditorState.create({
      doc: baseDoc,
      extensions: [
        markField,
        keymap.of(defaultKeymap),
        lineFactsGutter(state => ({ doc: state.doc, marks: state.field(markField), attribution, findings })),
      ],
    }),
  });
  let open = true;
  const handlers = {
    open: vi.fn(() => { open = true; }),
    dismiss: vi.fn(() => {
      if (!open) return false;
      open = false;
      return true;
    }),
    refresh: vi.fn(),
  };
  setLineFactsHandlers(view, handlers);

  const gutter = view.dom.appendChild(document.createElement("div"));
  gutter.className = "files-line-gutter";
  const cell = gutter.appendChild(document.createElement("div"));
  cell.className = "cm-gutterElement";

  const addGutterControl = (cls: string) => {
    const btn = cell.appendChild(document.createElement("button"));
    btn.className = cls;
    btn.dataset.line = "2";
    return btn;
  };
  const handle = addGutterControl("files-line-gutter__facts");
  const foldNumber = addGutterControl("files-line-gutter__number files-line-gutter__number--foldable");
  const findingGlyph = addGutterControl("files-line-gutter__finding");
  const provenanceBar = addGutterControl("files-line-gutter__provenance");
  const code = view.contentDOM.querySelector<HTMLElement>(".cm-line")!;

  const card = document.body.appendChild(document.createElement("div"));
  card.className = "files-line-facts";
  card.dataset.denAnchoredSurface = "card";
  const cardRow = card.appendChild(document.createElement("button"));
  cardRow.className = "files-line-facts__row";

  const outside = document.body.appendChild(document.createElement("button"));

  return { setMarks, setOpen: (value: boolean) => { open = value; }, handlers, targets: { handle, cardRow, foldNumber, findingGlyph, provenanceBar, code, outside } };
}

describe("line facts gutter", () => {
  it.each([
    ["handle", "handle", 0],
    ["card row", "cardRow", 0],
    ["fold number", "foldNumber", 1],
    ["finding glyph", "findingGlyph", 1],
    ["provenance bar", "provenanceBar", 1],
    ["code", "code", 1],
    ["outside the editor", "outside", 1],
  ] as const)("press on %s calls dismiss %i times", (_label, key, expected) => {
    const { handlers, targets } = mountPlugin();
    targets[key].dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(expected);
  });

  it("dismisses once on scroll, editor Escape before selection collapse, and focus leaving editor and card, while yielding to front layers", () => {
    const { setMarks, setOpen, handlers, targets } = mountPlugin();
    view!.scrollDOM.dispatchEvent(new Event("scroll"));
    expect(handlers.dismiss).toHaveBeenCalledTimes(1);

    handlers.dismiss.mockClear();
    setOpen(true);
    view!.dispatch({ selection: EditorSelection.range(0, 3) });
    view!.contentDOM.focus();
    const front = document.body.appendChild(document.createElement("div"));
    front.dataset.denAnchoredSurface = "menu";
    view!.contentDOM.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(0);
    front.remove();

    view!.dispatch({ selection: EditorSelection.range(0, 3) });
    view!.contentDOM.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(1);
    expect(view!.state.selection.main.from).toBe(0);
    expect(view!.state.selection.main.to).toBe(3);

    // Once closed, Escape falls through to simplifySelection.
    view!.contentDOM.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    expect(view!.state.selection.main.empty).toBe(true);

    handlers.dismiss.mockClear();
    setOpen(true);
    const panelInput = view!.dom.appendChild(document.createElement("input"));
    panelInput.addEventListener("keydown", event => { if (event.key === "Escape") event.preventDefault(); });
    panelInput.focus();
    panelInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(0);
    panelInput.remove();

    targets.cardRow.focus();
    targets.cardRow.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(0);

    view!.contentDOM.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true, cancelable: true }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(0);

    targets.handle.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: targets.code }));
    targets.handle.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: targets.cardRow }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(0);

    targets.handle.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: targets.outside }));
    expect(handlers.dismiss).toHaveBeenCalledTimes(1);

    view!.dispatch({ effects: setMarks.of(paintedFrom({ reads: [{ id: "r", sequence: 1, tool_call_id: "c", tool: "read", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 2 }], stale: false }] })) });
    expect(handlers.refresh).toHaveBeenCalledTimes(1);
  });

  it("attaches the line hint to every handle and replaces markers when lead or count changes", () => {
    const finding = (id: string, level: SecurityFinding["level"]): SecurityFinding => ({
      rule_id: id, level, message: `msg-${id}`, locations: [], fingerprints: { primary: id }, tool: { name: "opengrep", driver_id: "opengrep" },
    });
    const cases: Array<{ name: string; input: LineFactsInput; emptyLine?: number }> = [
      {
        name: "approval-held change",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ intents: [{ id: "i", tool_call_id: "t", checkpoint_id: "cp", tool: "replace_lines", operation: "edit", state: "awaiting_approval", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }] }] }) },
      },
      {
        name: "pending change",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ intents: [{ id: "i", tool_call_id: "t", tool: "replace_lines", operation: "edit", state: "pending", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }] }] }) },
      },
      {
        name: "pending insertion",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ intents: [{ id: "i", tool_call_id: "t", tool: "insert", operation: "edit", state: "pending", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 2, start_character: 0, end_character: 0 }] }] }) },
      },
      {
        name: "pending change with destination",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ intents: [{ id: "i", tool_call_id: "t", tool: "move", operation: "move", to_path: "b.ts", state: "pending", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }] }] }) },
      },
      {
        name: "worker draft ready",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ worker_drafts: [{ worker_id: "w", state: "ready", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], insertions: 3, deletions: 1 }] }) },
      },
      {
        name: "worker draft landing",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ worker_drafts: [{ worker_id: "w", state: "landing", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], insertions: 3, deletions: 1 }] }) },
      },
      {
        name: "read range",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ reads: [{ id: "r", sequence: 1, tool_call_id: "c", tool: "read", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false }] }) },
      },
      {
        name: "read whole file",
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ reads: [{ id: "r", sequence: 1, tool_call_id: "c", tool: "read", root_id: "root", path: "a.ts", extent: "whole_file", ranges: [], stale: false }] }) },
      },
      {
        name: "read stale",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ reads: [{ id: "r", sequence: 1, tool_call_id: "c", tool: "read", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: true }] }) },
      },
      {
        name: "search match",
        emptyLine: 4,
        input: { doc: baseDoc, attribution: [], findings: [], marks: paintedFrom({ reads: [{ id: "r", sequence: 1, tool_call_id: "c", tool: "search", root_id: "root", path: "a.ts", extent: "matches", ranges: [{ start_line: 2, end_line: 3 }], stale: false }] }) },
      },
      {
        name: "edited by chat",
        emptyLine: 4,
        input: { doc: baseDoc, marks: [], findings: [], attribution: [{ startLine: 2, endLine: 3, contributors: [{ sessionId: "s", turn: 4, ts: new Date(Date.now() - 600_000).toISOString(), toolCallId: "tc" }] }] },
      },
      {
        name: "finding",
        emptyLine: 4,
        input: { doc: baseDoc, marks: [], attribution: [], findings: [{ line: 2, level: "high", findings: [finding("f1", "high")] }] },
      },
      {
        name: "two rows",
        emptyLine: 4,
        input: { doc: baseDoc, marks: [], attribution: [], findings: [{ line: 2, level: "high", findings: [finding("f1", "high"), finding("f2", "low")] }] },
      },
      {
        name: "three rows",
        emptyLine: 4,
        input: { doc: baseDoc, marks: [], attribution: [{ startLine: 2, endLine: 2, contributors: [{ sessionId: "s", turn: 1, ts: "", toolCallId: "tc" }] }], findings: [{ line: 2, level: "high", findings: [finding("f1", "high"), finding("f2", "low")] }] },
      },
    ];

    for (const { input, emptyLine } of cases) {
      const marker = lineFactsMarker(input, 2);
      expect(marker).not.toBeNull();
      const dom = marker!.toDOM();
      expect(dom.dataset.tip).toBe(lineFactsHintLabel(lineFactsHint(lineFacts(2, input))!));
      expect(dom.dataset.tipPos).toBe("end");
      if (emptyLine) expect(lineFactsMarker(input, emptyLine)).toBeNull();
    }

    const oneFinding = cases.find(c => c.name === "finding")!.input;
    const differentLead: LineFactsInput = {
      doc: baseDoc, marks: [], attribution: [],
      findings: [{ line: 2, level: "critical", findings: [finding("f-other", "critical")] }],
    };
    const twoFindings = cases.find(c => c.name === "two rows")!.input;
    const baseMarker = lineFactsMarker(oneFinding, 2)!;
    expect(baseMarker.eq(lineFactsMarker(oneFinding, 2)!)).toBe(true);
    expect(baseMarker.eq(lineFactsMarker(differentLead, 2)!)).toBe(false);
    expect(baseMarker.eq(lineFactsMarker(twoFindings, 2)!)).toBe(false);
  });

  it("marks read gutter handles with read classification", () => {
    const marks = paintedFrom({
      reads: [{ id: "read", sequence: 1, tool_call_id: "call", tool: "read", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false }],
    }, "reader");
    const input: LineFactsInput = { doc: baseDoc, marks, attribution: [], findings: [] };
    const markerLine2 = lineFactsMarker(input, 2);
    expect(markerLine2?.hasRead).toBe(true);
    expect(markerLine2?.elementClass).toContain("files-line-gutter__cell--read");
    const dom = markerLine2?.toDOM();
    expect(dom?.classList.contains("files-line-gutter__facts--read")).toBe(true);
    expect(dom?.dataset.read).toBe("true");
    expect(lineFactsMarker(input, 1)).toBeNull();
  });

  it("provides a single uniform tint across overlapping reads without stacking gradients", () => {
    const marks: PaintedAgentMark[] = [
      ...paintedFrom({ reads: [{ id: "read1", sequence: 1, tool_call_id: "call1", tool: "read", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false }] }, "reader1"),
      ...paintedFrom({ reads: [{ id: "read2", sequence: 2, tool_call_id: "call2", tool: "read", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 4 }], stale: false }] }, "reader2"),
    ];
    const input: LineFactsInput = { doc: baseDoc, marks, attribution: [], findings: [] };
    const tintLine2 = lineFactsTint(input, 2);
    // Overlapping reads paint one gradient, not stacked ones.
    expect(tintLine2.startsWith("linear-gradient")).toBe(true);
    expect(tintLine2.includes("), linear-gradient")).toBe(false);
    expect(lineFactsTint(input, 3)).toBe(tintLine2);
  });

  it("suppresses range read gradients when a whole-file read is already active", () => {
    const marks = paintedFrom({
      reads: [
        { id: "whole", sequence: 1, tool_call_id: "whole", tool: "read", root_id: "root", path: "a.ts", extent: "whole_file", ranges: [], stale: false },
        { id: "range", sequence: 2, tool_call_id: "range", tool: "read", root_id: "root", path: "a.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }], stale: false },
      ],
    }, "reader");
    const input: LineFactsInput = { doc: baseDoc, marks, attribution: [], findings: [] };
    expect(lineFactsTint(input, 2)).toBe("none");
    const marker = lineFactsMarker(input, 2);
    expect(marker?.hasRead).toBe(true);
    expect(marker?.tint).toBe("none");
  });
});
