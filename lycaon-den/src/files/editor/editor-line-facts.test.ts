// @vitest-environment jsdom
import { EditorSelection } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { createRoot } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { setLineGutterAttribution } from "../../components/source/annotations/line-gutter.ts";
import { createSourceEditorState } from "../../components/source/editor/codemirror-theme.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";
import { createEditorLineFacts } from "./editor-line-facts.ts";

let view: EditorView | undefined;
let dispose: (() => void) | undefined;

afterEach(() => {
  dispose?.();
  dispose = undefined;
  view?.destroy();
  view = undefined;
  document.body.replaceChildren();
});

describe("editor line facts controller", () => {
  it("toggles on handle click, enters on keyboard activation, and dismisses on editor Escape without collapsing selection", () => {
    const parent = document.body.appendChild(document.createElement("div"));
    view = new EditorView({
      parent,
      state: createSourceEditorState({ surface: "files", doc: "alpha\nbeta\ngamma\n" }),
    });
    setLineGutterAttribution(view, [{
      startLine: 2,
      endLine: 2,
      contributors: [{ sessionId: "s1", turn: 3, ts: "2026-11-11T00:00:00Z", toolCallId: "tc1", effectId: "eff-1" }],
    }]);

    const buffer = { key: "root:main.go", rootId: "root", path: "main.go", dirty: false } as ReturnType<FilesEditorScope["buffer"]>;
    const facts = createRoot(fn => {
      dispose = fn;
      return createEditorLineFacts({
        projectId: () => "proj-1",
        buffer: () => buffer,
        client: () => null,
        currentView: () => view,
        editorView: () => view,
        cursorLine: () => 1,
        findingsScanId: () => "",
        revealInTranscript: () => undefined,
        rejectHunk: () => undefined,
      });
    });
    facts.installGutterInteractions(view);

    expect(facts.hideLineFacts()).toBe(false);
    const handle = view.dom.querySelector<HTMLButtonElement>('.files-line-gutter__facts[data-line="2"]')!;
    const cell = handle.closest<HTMLElement>(".cm-gutterElement")!;

    handle.click();
    expect(facts.lineFactsCard()).toMatchObject({ line: 2, path: "main.go", enter: false, projectId: "proj-1" });
    expect(handle.getAttribute("aria-expanded")).toBe("true");
    expect(cell.classList.contains("is-open")).toBe(true);

    // Clicking the open handle closes the card.
    handle.click();
    expect(facts.lineFactsCard()).toBeNull();
    expect(handle.getAttribute("aria-expanded")).toBe("false");
    expect(cell.classList.contains("is-open")).toBe(false);

    handle.focus();
    handle.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    expect(facts.lineFactsCard()).toMatchObject({ line: 2, enter: true });
    facts.returnLineFactsFocus();
    expect(facts.lineFactsCard()).toBeNull();
    expect(document.activeElement).toBe(handle);

    view.dispatch({ selection: EditorSelection.range(0, 4) });
    view.contentDOM.focus();
    handle.click();
    expect(facts.lineFactsCard()).not.toBeNull();

    view.contentDOM.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    expect(facts.lineFactsCard()).toBeNull();
    expect(view.state.selection.main.from).toBe(0);
    expect(view.state.selection.main.to).toBe(4);
  });
});
