// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import { emphasizeAndScrollToLine } from "./codemirror-theme.ts";
import { parseProsePathCandidate } from "../../../chat/markdown/prose-path-parse.ts";
import { resolveSourceRequest } from "../../../platform/navigation/open-source.ts";

describe("selection range reveal round trip", () => {
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("parses prose path:start-end into open-source endLine", () => {
    const parsed = parseProsePathCandidate("src/main.ts:12-34");
    expect(parsed).toEqual({
      path: "src/main.ts",
      line: 12,
      endLine: 34,
    });
    const resolved = resolveSourceRequest(
      {
        intent: "transient",
        projectId: "p1",
        path: parsed!.path,
        line: parsed!.line,
        endLine: parsed!.endLine,
      },
      () => ({
        roots: [{ id: "r1", path: "/repo" }],
      }),
    );
    expect(resolved).toMatchObject({
      status: "resolved",
      request: {
        path: "src/main.ts",
        line: 12,
        endLine: 34,
        rootId: "r1",
      },
    });
  });

  it("selects the inclusive line range in the editor", () => {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const view = new EditorView({
      state: EditorState.create({
        doc: "one\ntwo\nthree\nfour\nfive\n",
      }),
      parent,
    });
    emphasizeAndScrollToLine(view, 2, 4);
    const sel = view.state.selection.main;
    expect(view.state.doc.lineAt(sel.from).number).toBe(2);
    expect(view.state.doc.lineAt(sel.to - 1).number).toBe(4);
    view.destroy();
  });

  it("lands a single line as a caret, not a whole-line selection", () => {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const view = new EditorView({
      state: EditorState.create({
        doc: "one\ntwo\nthree\n",
      }),
      parent,
    });
    emphasizeAndScrollToLine(view, 2);
    const sel = view.state.selection.main;
    expect(sel.empty).toBe(true);
    expect(view.state.doc.lineAt(sel.head).number).toBe(2);
    view.destroy();
  });
});
