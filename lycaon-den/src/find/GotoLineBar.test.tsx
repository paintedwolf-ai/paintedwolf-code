// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { GotoLineBar } from "./GotoLineBar.tsx";
import {
  gotoLineController,
  openGotoLine,
  resetGotoLineForTests,
} from "./goto-line-controller.ts";
import { editorCommandBarPlacement } from "./find-controller.ts";

afterEach(() => resetGotoLineForTests());

function editor(): EditorView {
  return new EditorView({
    state: EditorState.create({ doc: "one\ntwo\nthree\nfour\n" }),
  });
}

describe("GotoLineBar", () => {
  it("moves to a line without creating a submitting form", () => {
    const view = editor();
    view.dispatch({ selection: { anchor: view.state.doc.line(3).from } });
    openGotoLine(view);
    render(() => <GotoLineBar />);

    const bar = screen.getByTestId("goto-line-bar");
    const input = screen.getByTestId("goto-line-input");
    const submit = screen.getByTestId("goto-line-submit");
    expect(bar.classList.contains("den-editor-command-bar")).toBe(true);
    expect(bar.querySelector("form")).toBeNull();
    expect(submit.getAttribute("type")).toBe("button");

    fireEvent.input(input, { target: { value: "1" } });
    fireEvent.click(submit);

    expect(view.state.doc.lineAt(view.state.selection.main.head).number).toBe(1);
    expect(screen.queryByTestId("goto-line-bar")).toBeNull();
    view.destroy();
  });

  it("shows invalid input in place", () => {
    const view = editor();
    openGotoLine(view);
    render(() => <GotoLineBar />);

    const input = screen.getByTestId("goto-line-input");
    fireEvent.input(input, { target: { value: "not a line" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByRole("status").textContent).toContain(
      "Enter a line number",
    );
    expect(gotoLineController.isOpen()).toBe(true);
    view.destroy();
  });

  it("accepts line and line-column positions only", () => {
    const view = editor();
    openGotoLine(view);
    render(() => <GotoLineBar />);

    const input = screen.getByTestId("goto-line-input");
    fireEvent.input(input, { target: { value: "2:2" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(view.state.selection.main.head).toBe(view.state.doc.line(2).from + 1);
    expect(screen.queryByTestId("goto-line-bar")).toBeNull();
    view.destroy();
  });

  it("rejects relative and percentage positions", () => {
    const view = editor();
    openGotoLine(view);
    render(() => <GotoLineBar />);

    const input = screen.getByTestId("goto-line-input");
    for (const value of ["0", "2:0", "+2", "50%"] as const) {
      fireEvent.input(input, { target: { value } });
      fireEvent.keyDown(input, { key: "Enter" });
      expect(input.getAttribute("aria-invalid")).toBe("true");
      expect(gotoLineController.isOpen()).toBe(true);
    }
    view.destroy();
  });

  it("reuses one files-editor surface when opened repeatedly", () => {
    const view = editor();
    render(() => <GotoLineBar />);
    openGotoLine(view);
    openGotoLine(view);

    expect(screen.getAllByTestId("goto-line-bar")).toHaveLength(1);
    expect(editorCommandBarPlacement()).toBe("files-editor");
    view.destroy();
  });
});
