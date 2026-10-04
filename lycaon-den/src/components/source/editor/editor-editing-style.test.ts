// @vitest-environment jsdom
import { DocumentHistory } from "../../../files/history/document-history.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EditorState, EditorSelection, Transaction } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { defaultKeymap, history } from "@codemirror/commands";
import { javascript } from "@codemirror/lang-javascript";
import { Vim, getCM, type CodeMirrorV } from "@replit/codemirror-vim";
import { editorEditingStyle, applyEditorEditingStyle } from "./editor-editing-style.ts";
import { editorHistory } from "./editor-history.ts";
import { resetEditorPrefsForTests } from "../../../settings/editor/editor-prefs.ts";
import { seedStockFrame } from "../../../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../../../contributions/contribution-store.ts";
import { COMMAND_DECLINED, registerCommandHandler, resetDispatcherForTests, setFilesStageActive, setDispatcherPlatformForTests, setShortcutOverrides } from "../../../shortcuts/dispatcher.ts";
import { buildEditorCommandBridge } from "./editor-command-bridge.ts";

let view: EditorView;
beforeEach(() => { setDispatcherPlatformForTests("macos"); resetEditorPrefsForTests(); seedStockFrame(); setFilesStageActive(true); });
afterEach(() => { view?.destroy(); document.body.replaceChildren(); resetDispatcherForTests(); resetContributionStoreForTest(); });
function create(style: "standard" | "emacs" | "vim", step?: (direction: "undo" | "redo") => void) {
  resetEditorPrefsForTests({ keymap: style });
  view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "one two\nthree four\n",
    extensions: [javascript(), EditorState.allowMultipleSelections.of(true), step ? editorHistory.of(step) : history(),
      editorEditingStyle(), buildEditorCommandBridge("macos"), keymap.of(defaultKeymap)] }) });
  view.focus();
}
function key(key: string, init: KeyboardEventInit = {}) {
  view.contentDOM.dispatchEvent(new KeyboardEvent("keydown", { key, code: key.length === 1 ? `Key${key.toUpperCase()}` : key,
    bubbles: true, cancelable: true, ...init }));
}

describe("editing styles", () => {
  it("supports Emacs mark, kill, and yank", () => {
    create("emacs");
    key("f", { ctrlKey: true });
    expect(view.state.selection.main.head).toBe(1);
    expect(view.dom.querySelector(".cm-vimCursorLayer, .cm-panels, .den-editor-mode")).toBeNull();
    key(" ", { ctrlKey: true, code: "Space" });
    key("f", { ctrlKey: true });
    expect(view.state.selection.main.from).toBe(1);
    expect(view.state.selection.main.to).toBe(2);
    key("w", { ctrlKey: true });
    expect(view.state.doc.toString()).toBe("oe two\nthree four\n");
    key("y", { ctrlKey: true });
    expect(view.state.doc.toString()).toBe("one two\nthree four\n");
  });

  it("retains language indentation and leaves composition events untouched", () => {
    create("emacs");
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: "function run() {" } });
    view.dispatch({ selection: { anchor: view.state.doc.length } });
    key("Enter");
    expect(view.state.doc.toString()).toBe("function run() {\n  ");
    const before = view.state.selection.main.head;
    key("b", { ctrlKey: true, isComposing: true });
    expect(view.state.selection.main.head).toBe(before);
  });

  it("routes Emacs file commands and history to their handlers", () => {
    const undo = vi.fn(), save = vi.fn(), search = vi.fn();
    registerCommandHandler("files.save", save);
    registerCommandHandler("search.openActions", search);
    create("emacs", undo);
    key("x", { ctrlKey: true }); key("s", { ctrlKey: true });
    expect(save).toHaveBeenCalledOnce();
    key("/", { ctrlKey: true, code: "Slash" });
    expect(undo).toHaveBeenCalledWith("undo");
    key("x", { altKey: true });
    expect(search).toHaveBeenCalledOnce();
  });

  it("runs Vim operators, repeat, visual selection and host history", () => {
    const historyStep = vi.fn();
    create("vim", historyStep);
    const cm = getCM(view)!;
    Vim.handleKey(cm, "d", "user"); Vim.handleKey(cm, "w", "user");
    expect(view.state.doc.toString()).toBe("two\nthree four\n");
    Vim.handleKey(cm, ".", "user");
    expect(view.state.doc.toString()).toBe("\nthree four\n");
    Vim.handleKey(cm, "u", "user");
    expect(historyStep).toHaveBeenCalledWith("undo");
    Vim.handleKey(cm, "<C-r>", "user");
    expect(historyStep).toHaveBeenCalledWith("redo");
  });

  it("selects text with visual motions", () => {
    create("vim");
    const cm = getCM(view)!;
    Vim.handleKey(cm, "v", "user"); Vim.handleKey(cm, "l", "user");
    expect(view.state.selection.main.empty).toBe(false);
  });

  it("routes Vim write and close through app commands", () => {
    const save = vi.fn(), close = vi.fn();
    registerCommandHandler("files.save", save); registerCommandHandler("files.saveAndClose", close);
    create("vim");
    Vim.handleEx(getCM(view)! as CodeMirrorV, "w"); Vim.handleEx(getCM(view)! as CodeMirrorV, "wq");
    expect(save).toHaveBeenCalledOnce(); expect(close).toHaveBeenCalledOnce();
  });

  it("leaves the standard style to the editor's own platform keys", () => {
    create("standard");
    expect(getCM(view)).toBeNull();
    expect(view.dom.querySelector(".cm-vimCursorLayer, .cm-panels, .den-editor-mode")).toBeNull();
    key("k", { ctrlKey: true });
    key("y", { ctrlKey: true });
    expect(view.state.doc.toString()).toBe("one two\nthree four\n");
    key("a", { ctrlKey: true });
    expect(view.state.selection.main.from).toBe(0);
    expect(view.state.selection.main.to).toBe(view.state.doc.length);
  });

  it("removes Emacs keys when the style changes to standard", () => {
    create("emacs");
    key("k", { ctrlKey: true });
    expect(view.state.doc.toString()).toBe("\nthree four\n");
    applyEditorEditingStyle(view, "standard", false);
    view.dispatch({ selection: { anchor: view.state.doc.length } });
    key("y", { ctrlKey: true });
    expect(view.state.doc.toString()).toBe("\nthree four\n");
  });

  it("changes style without replacing text or selections", () => {
    create("emacs");
    view.dispatch({ selection: EditorSelection.single(1, 3) });
    applyEditorEditingStyle(view, "vim", false);
    expect(view.state.doc.toString()).toBe("one two\nthree four\n");
    expect(getCM(view)).not.toBeNull();
    applyEditorEditingStyle(view, "emacs", true);
    expect(view.state.selection.main.from).toBe(1);
    expect(view.state.selection.main.to).toBe(3);
  });

  it("rotates the kill ring through document history without duplicating text", () => {
    resetEditorPrefsForTests({ keymap: "emacs" });
    const historyDoc = new DocumentHistory(); historyDoc.reset("alpha\nbeta\n");
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: historyDoc.state.doc,
      extensions: [editorEditingStyle(), editorHistory.of(direction => {
        historyDoc.step(direction, transaction => {
          historyDoc.state = transaction.state;
          view.dispatch({ changes: transaction.changes, selection: transaction.selection,
            annotations: Transaction.addToHistory.of(false) });
        });
      }), EditorView.updateListener.of(update => {
        for (const transaction of update.transactions) {
          if (transaction.annotation(Transaction.addToHistory) === false) continue;
          historyDoc.apply({ changes: transaction.changes, selection: transaction.selection });
        }
      })] }) });
    view.focus();
    key("k", { ctrlKey: true });
    view.dispatch({ selection: { anchor: 1 } });
    key("f", { ctrlKey: true }); key("b", { ctrlKey: true });
    key("k", { ctrlKey: true });
    key("y", { ctrlKey: true });
    expect(view.state.doc.toString()).toBe("\nbeta\n");
    key("y", { altKey: true });
    expect(view.state.doc.toString()).toBe("\nalpha\n");
  });

  it("honors an explicit shortcut override before the editing style", () => {
    const select = vi.fn(); registerCommandHandler("editor.selectAll", select);
    const overrides = { "painted-wolf/platform:key-editor-select-all": "Ctrl+F" };
    setShortcutOverrides(overrides);
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "one two",
      extensions: [buildEditorCommandBridge("macos", overrides), editorEditingStyle()] }) });
    view.focus();
    key("f", { ctrlKey: true });
    expect(select).toHaveBeenCalledOnce();
    expect(view.state.selection.main.head).toBe(0);
  });

  it("lets app navigation sequences take priority over modal motions", () => {
    const chat = vi.fn(); registerCommandHandler("go.chat", chat);
    create("vim");
    key(";", { metaKey: true, code: "Semicolon" });
    key("c");
    expect(chat).toHaveBeenCalledOnce();
    expect(view.state.doc.toString()).toBe("one two\nthree four\n");
  });

  it("offers a declined override to its handler once", () => {
    const select = vi.fn(() => COMMAND_DECLINED);
    registerCommandHandler("editor.selectAll", select);
    // F8 is the stock next-finding chord; a colliding override would be ambiguous.
    const overrides = { "painted-wolf/platform:key-editor-select-all": "F9" };
    setShortcutOverrides(overrides);
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "one two",
      extensions: [buildEditorCommandBridge("macos", overrides), editorEditingStyle()] }) });
    view.focus();
    key("F9");
    expect(select).toHaveBeenCalledOnce();
    expect(view.state.selection.main.empty).toBe(true);
  });

  it("routes Inline edit before the fallback syntax-selection binding", () => {
    const inline = vi.fn(); registerCommandHandler("editor.inlineEdit", inline);
    create("emacs");
    key("i", /Mac/.test(navigator.platform) ? { metaKey: true } : { ctrlKey: true });
    expect(inline).toHaveBeenCalledOnce();
  });
});
