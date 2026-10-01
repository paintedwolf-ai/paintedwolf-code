import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { TextEditContextMenuHost } from "../components/TextEditContextMenuHost.tsx";
import {
  selectAllEditable,
  snapshotEditable,
  selectedTextFromSnapshot,
} from "./interaction/text-edit-context.ts";

describe("select-all then right-click keeps selection", () => {
  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
    window.getSelection()?.removeAllRanges();
  });

  it("keeps prose selection after Select all", async () => {
    const prose = document.createElement("p");
    prose.dataset.testid = "prose";
    prose.style.userSelect = "text";
    prose.textContent = "keep me";
    document.body.appendChild(prose);
    const range = document.createRange();
    range.selectNodeContents(prose);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);

    render(() => <TextEditContextMenuHost />);
    fireEvent.contextMenu(prose, { clientX: 10, clientY: 10, button: 2 });
    fireEvent.click(await screen.findByTestId("text-edit-select-all"));
    expect(window.getSelection()?.toString()).toBe("keep me");

    fireEvent.contextMenu(prose, { clientX: 12, clientY: 12, button: 2 });
    await Promise.resolve();
    const copy = await screen.findByTestId("text-edit-copy");
    expect((copy as HTMLButtonElement).disabled).toBe(false);
    expect(window.getSelection()?.toString()).toBe("keep me");
  });

  it("selectAllEditable updates CodeMirror EditorSelection", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const view = new EditorView({
      parent: host,
      state: EditorState.create({ doc: "alpha beta gamma" }),
    });
    selectAllEditable(snapshotEditable(view.contentDOM));
    expect(view.state.selection.main.from).toBe(0);
    expect(view.state.selection.main.to).toBe(view.state.doc.length);
    const snap = snapshotEditable(view.contentDOM);
    expect(selectedTextFromSnapshot(snap)).toBe("alpha beta gamma");
    view.destroy();
  });

  it("CodeMirror Select all then context menu offers enabled Copy", async () => {
    const host = document.createElement("div");
    host.dataset.testid = "cm-host";
    document.body.appendChild(host);
    const view = new EditorView({
      parent: host,
      state: EditorState.create({ doc: "alpha beta gamma" }),
    });

    render(() => <TextEditContextMenuHost />);
    fireEvent.contextMenu(view.contentDOM, {
      clientX: 10,
      clientY: 10,
      button: 2,
    });
    fireEvent.click(await screen.findByTestId("text-edit-select-all"));
    expect(view.state.selection.main.to).toBe(view.state.doc.length);

    fireEvent.contextMenu(view.contentDOM, {
      clientX: 12,
      clientY: 12,
      button: 2,
    });
    await Promise.resolve();
    const copy = await screen.findByTestId("text-edit-copy");
    expect((copy as HTMLButtonElement).disabled).toBe(false);

    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    fireEvent.click(copy);
    expect(writeText).toHaveBeenCalledWith("alpha beta gamma");
    view.destroy();
  });
});
