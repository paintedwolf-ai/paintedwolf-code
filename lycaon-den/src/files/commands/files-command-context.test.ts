// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import { createComputed, createRoot } from "solid-js";
import { filesCommandContext } from "./files-command-context.ts";
import { beginFilesEditorAttach, completeFilesEditorAttach, mountedFilesEditorView,
  notifyFilesEditorViewChanged, resetFilesEditorHostForTests, setFilesEditorPresented,
  softDetachFilesEditor } from "../editor/files-editor-host.ts";
import { createSourceEditorState } from "../../components/source/editor/codemirror-theme.ts";

const buffer = { key: "file", rootId: "root", path: "orchard.ts", documentRevision: 7, writable: true };
afterEach(() => resetFilesEditorHostForTests());

function attach(doc: string) {
  const host = document.createElement("div");
  const handlers = { onCursorChange: notifyFilesEditorViewChanged };
  const view = new EditorView({ state: createSourceEditorState({ surface: "files", doc, handlers }), parent: host });
  completeFilesEditorAttach({ projectId: "project", key: buffer.key, path: buffer.path, view, handlers, host });
  return { view, host };
}

describe("editor command targets", () => {
  it("uses only host grammar metadata for the current path", () => {
    const { view } = attach("orchard");
    const language = { path: buffer.path, name: "typescript" };
    expect(filesCommandContext({ ...buffer, language }, view).facts.language).toBe("typescript");
    expect(filesCommandContext({ ...buffer, path: "orchard.py", language }, view).facts.language).toBeNull();
    expect(filesCommandContext(buffer, view).facts.language).toBeNull();
  });
  it("republishes selection facts when the anchor moves but the caret stays put", () => {
    const { view } = attach("orchard");
    view.dispatch({ selection: { anchor: 7 } });
    createRoot((dispose) => {
      let selected = false;
      createComputed(() => {
        const mounted = mountedFilesEditorView("project", buffer.key);
        selected = mounted ? filesCommandContext(buffer, mounted).facts.hasSelection : false;
      });
      expect(selected).toBe(false);
      view.dispatch({ selection: { anchor: 0, head: 7 } });
      expect(selected).toBe(true);
      view.dispatch({ selection: { anchor: 7 } });
      expect(selected).toBe(false);
      dispose();
    });
  });
  it.each([{ anchor: 0, head: 4 }, { anchor: 4, head: 0 }])("keeps exclusive line boundaries for $anchor → $head", (selection) => {
    const { view } = attach("one\ntwo\nthree");
    view.dispatch({ selection });
    const target = filesCommandContext(buffer, view);
    expect(target.facts.hasSelection).toBe(true);
    expect(target.context).toEqual({ root_id: "root", path: "orchard.ts", document_revision: 7, start_line: 1, end_line: 1 });
  });

  it("distinguishes a caret from a selection on the same line and retains symbol coordinates", () => {
    const { view } = attach("orchard");
    view.dispatch({ selection: { anchor: 7 } });
    expect(filesCommandContext(buffer, view).context).not.toHaveProperty("end_line");
    view.dispatch({ selection: { anchor: 0, head: 7 } });
    const target = filesCommandContext(buffer, view);
    expect(target.facts).toMatchObject({ hasSelection: true, symbol: "orchard" });
    expect(target.context).toMatchObject({ symbol: "orchard", start_line: 1, end_line: 1 });
  });

  it("removes unavailable views and restores the same retained selection on reattachment", () => {
    const { view, host } = attach("orchard");
    view.dispatch({ selection: { anchor: 0, head: 7 } });
    expect(mountedFilesEditorView("other-project", buffer.key)).toBeNull();
    setFilesEditorPresented("project", buffer.key, false);
    expect(mountedFilesEditorView("project", buffer.key)).toBeNull();
    setFilesEditorPresented("project", buffer.key, true);
    softDetachFilesEditor("project", buffer.key, host);
    expect(mountedFilesEditorView("project", buffer.key)).toBeNull();
    beginFilesEditorAttach({ projectId: "project", key: buffer.key, path: buffer.path, draft: "orchard", host });
    expect(mountedFilesEditorView("project", buffer.key)).toBe(view);
    expect(filesCommandContext(buffer, view).facts.hasSelection).toBe(true);
  });

  it("does not claim a read-only view can edit", () => {
    const { view } = attach("orchard");
    view.setState(EditorState.create({ doc: "orchard", extensions: EditorState.readOnly.of(true) }));
    expect(filesCommandContext(buffer, view).facts.editable).toBe(false);
  });
});
