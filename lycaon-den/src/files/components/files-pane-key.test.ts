// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { editorScrollPosition, setEditorScrollTop } from "../../components/source/editor/editor-scroll-position.ts";
import {
  filesPaneKey,
  parseFilesPaneKey,
} from "./files-pane-key.ts";
import { fileBufferKey } from "./project-files-model.ts";
import { FILE_BUFFER_KINDS } from "../documents/project-files-buffer-kind.ts";

describe("filesPaneKey", () => {
  it.each(FILE_BUFFER_KINDS)("routes every supported %s pane", kind => {
    const key = 'tool:["session","message","call","output"]';
    expect(parseFilesPaneKey(filesPaneKey(key, kind))).toEqual({ key, kind });
  });
  it("round-trips key and kind through a primitive pane id", () => {
    const key = fileBufferKey("r1", "src/main.ts");
    const pane = filesPaneKey(key, "text");
    expect(pane).toBe(`${key}\0text`);
    expect(parseFilesPaneKey(pane)).toEqual({ key, kind: "text" });
  });

  it("keeps job-scoped keys distinct from kind", () => {
    const key = fileBufferKey("r1", "src/main.ts", "job-1");
    const pane = filesPaneKey(key, "diff");
    expect(parseFilesPaneKey(pane)).toEqual({ key, kind: "diff" });
  });

  it("carries a walk page keyed by its step", () => {
    const pane = filesPaneKey("walk:git:t1", "walk");
    expect(parseFilesPaneKey(pane)).toEqual({ key: "walk:git:t1", kind: "walk" });
  });

  it("rejects unknown kinds", () => {
    expect(parseFilesPaneKey("r1\0src/a.ts\0nope")).toBeNull();
  });
});

describe("files editor host", () => {
  it("reparents the same EditorView across soft detach and attach", async () => {
    const {
      beginFilesEditorAttach,
      completeFilesEditorAttach,
      softDetachFilesEditor,
      getFilesEditorView,
      resetFilesEditorHostForTests,
    } = await import("../editor/files-editor-host.ts");
    resetFilesEditorHostForTests();

    const hostA = document.createElement("div");
    const hostB = document.createElement("div");
    document.body.appendChild(hostA);
    document.body.appendChild(hostB);

    const projectId = "p-host";
    const key = fileBufferKey("r1", "a.ts");
    const plan = beginFilesEditorAttach({
      projectId,
      key,
      path: "a.ts",
      draft: "hello\n",
      host: hostA,
    });
    expect(plan.kind).toBe("fresh");
    const state = EditorState.create({ doc: "hello\n", extensions: [editorScrollPosition] });
    const view = new EditorView({ state, parent: hostA });
    completeFilesEditorAttach({
      projectId,
      key,
      path: "a.ts",
      view,
      handlers: plan.handlers,
      host: hostA,
    });

    expect(getFilesEditorView(projectId, key)).toBe(view);
    setEditorScrollTop(view, 42);
    expect(softDetachFilesEditor(projectId, key, hostA)).toBe(true);
    expect(hostA.contains(view.dom)).toBe(false);

    const again = beginFilesEditorAttach({
      projectId,
      key,
      path: "a.ts",
      draft: "hello\n",
      host: hostB,
    });
    expect(again.kind).toBe("reused");
    if (again.kind !== "reused") return;
    expect(again.view).toBe(view);
    expect(again.scrollTop).toBe(42);
    expect(hostB.contains(view.dom)).toBe(true);
    expect(view.scrollDOM.scrollTop).toBe(42);

    resetFilesEditorHostForTests();
    hostA.remove();
    hostB.remove();
  });
});
