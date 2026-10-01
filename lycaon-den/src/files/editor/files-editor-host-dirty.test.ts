// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import {
  completeFilesEditorAttach,
  isFilesEditorViewDocDirty,
  markFilesEditorViewDocDirty,
  resetFilesEditorHostForTests,
  settleFilesEditorView,
} from "./files-editor-host.ts";
import { fileBufferKey } from "../components/project-files-model.ts";

const PROJECT_ID = "project";
const KEY = fileBufferKey("root", "large.ts");

afterEach(() => resetFilesEditorHostForTests());

describe("Files editor host document tracking", () => {
  it("tracks unmaterialized input without copying it into the string cache", () => {
    const host = document.createElement("div");
    const view = new EditorView({
      state: EditorState.create({ doc: "base" }),
      parent: host,
    });
    completeFilesEditorAttach({
      projectId: PROJECT_ID,
      key: KEY,
      path: "large.ts",
      view,
      handlers: {},
      host,
    });

    markFilesEditorViewDocDirty(PROJECT_ID, KEY);
    expect(isFilesEditorViewDocDirty(PROJECT_ID, KEY)).toBe(true);

    settleFilesEditorView(PROJECT_ID, KEY);
    expect(isFilesEditorViewDocDirty(PROJECT_ID, KEY)).toBe(false);
  });
});
