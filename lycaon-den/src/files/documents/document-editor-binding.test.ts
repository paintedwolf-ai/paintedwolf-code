// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { history, historyKeymap } from "@codemirror/commands";
import { documentEditorBinding, documentHistoryKey } from "./document-editor-binding.ts";
import type { DocumentReplica } from "./document-replica.ts";
import { editorEditingStyle } from "../../components/source/editor/editor-editing-style.ts";
import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { setShortcutPlatformForTests } from "../../shortcuts/platform.ts";

const keys = (init: Partial<KeyboardEvent>) => ({ key: "", code: "", shiftKey: false, altKey: false, ctrlKey: false, metaKey: false, ...init });

describe("document history keys", () => {
  it("reads Shift from the event, not the reported key text", () => {
    expect(documentHistoryKey(keys({ key: "z", code: "KeyZ", ctrlKey: true }), "linux")).toBe("undo");
    expect(documentHistoryKey(keys({ key: "z", code: "KeyZ", ctrlKey: true, shiftKey: true }), "linux")).toBe("redo");
    expect(documentHistoryKey(keys({ key: "Z", code: "KeyZ", ctrlKey: true, shiftKey: true }), "windows")).toBe("redo");
    expect(documentHistoryKey(keys({ key: "z", code: "KeyZ", metaKey: true }), "macos")).toBe("undo");
    expect(documentHistoryKey(keys({ key: "z", code: "KeyZ", metaKey: true, shiftKey: true }), "macos")).toBe("redo");
  });

  it("leaves other chords to the editing style and the platform", () => {
    expect(documentHistoryKey(keys({ key: "z", code: "KeyZ", ctrlKey: true }), "macos")).toBeNull();
    expect(documentHistoryKey(keys({ key: "y", code: "KeyY", metaKey: true }), "macos")).toBeNull();
    expect(documentHistoryKey(keys({ key: "z", code: "KeyZ", metaKey: true }), "linux")).toBeNull();
    expect(documentHistoryKey(keys({ key: "z", code: "KeyZ", ctrlKey: true, altKey: true }), "linux")).toBeNull();
    expect(documentHistoryKey(keys({ key: "k", code: "KeyK", ctrlKey: true }), "linux")).toBeNull();
    expect(documentHistoryKey(keys({ key: "y", code: "KeyY", ctrlKey: true }), "linux")).toBeNull();
  });
});

describe("document history routing", () => {
  let view: EditorView | undefined;
  afterEach(() => { view?.destroy(); view = undefined; setShortcutPlatformForTests(null); resetEditorPrefsForTests(); });

  function mount(style: "standard" | "emacs") {
    setShortcutPlatformForTests("linux");
    resetEditorPrefsForTests({ keymap: style });
    const stepHistory = vi.fn();
    const replica = { stepHistory, attachEditor: vi.fn(), detachEditor: vi.fn(), acceptEditorTransaction: vi.fn(),
      history: { boundary: vi.fn() } } as unknown as DocumentReplica;
    view = new EditorView({ parent: document.body, state: EditorState.create({ doc: "one two\n",
      // The stock history keymap stands in for every binding an editable view carries.
      extensions: [editorEditingStyle(), history(), keymap.of(historyKeymap), documentEditorBinding(replica)] }) });
    const press = (init: KeyboardEventInit) => view!.contentDOM.dispatchEvent(new KeyboardEvent("keydown",
      { bubbles: true, cancelable: true, ...init }));
    return { stepHistory, press };
  }

  it("sends the standard style's undo and redo to the shared document history", () => {
    const { stepHistory, press } = mount("standard");
    press({ key: "z", code: "KeyZ", ctrlKey: true });
    press({ key: "z", code: "KeyZ", ctrlKey: true, shiftKey: true });
    press({ key: "y", code: "KeyY", ctrlKey: true });
    expect(stepHistory.mock.calls).toEqual([["undo"], ["redo"], ["redo"]]);
  });

  it("keeps Control+Y as Emacs yank", () => {
    const { stepHistory, press } = mount("emacs");
    press({ key: "y", code: "KeyY", ctrlKey: true });
    expect(stepHistory).not.toHaveBeenCalledWith("redo");
  });
});
