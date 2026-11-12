import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { EditorContext } from "../../api/types.ts";

const openSourceLocation = vi.hoisted(() => vi.fn(() => Promise.resolve({ status: "opened" })));
vi.mock("../../platform/navigation/open-source.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../platform/navigation/open-source.ts")>();
  return { ...actual, openSourceLocation };
});

import { EditorContextLine } from "./EditorContextLine.tsx";

const editor: EditorContext = {
  files: [
    { root_id: "root-a", path: "src/login.go", display_path: "src/login.go", unsaved: true, view: { start_line: 42, end_line: 88, cursor: false } },
    { root_id: "root-b", path: "guide.md", display_path: "@docs/guide.md", unsaved: false, view: { start_line: 7, end_line: 7, cursor: true } },
    { root_id: "root-a", path: "src/refresh.go", display_path: "src/refresh.go", unsaved: false },
  ],
  more_files: 4,
};

describe("EditorContextLine", () => {
  it("shows every recorded file with its lines and the remaining count", () => {
    render(() => <EditorContextLine editor={editor} projectId="project" />);
    const files = screen.getAllByTestId("transcript-editor-context-file");
    expect(files.map((file) => file.textContent?.replace("•", ""))).toEqual(["src/login.go:42-88", "@docs/guide.md:7", "src/refresh.go"]);
    expect(files.map((file) => file.hasAttribute("data-unsaved"))).toEqual([true, false, false]);
    expect(screen.getByTestId("transcript-editor-context-more").textContent).toContain(String(editor.more_files));
  });

  it("opens a file at its recorded selection", () => {
    render(() => <EditorContextLine editor={editor} projectId="project" />);
    fireEvent.click(screen.getAllByTestId("transcript-editor-context-file")[0]);
    expect(openSourceLocation).toHaveBeenLastCalledWith(expect.objectContaining({
      projectId: "project", rootId: "root-a", path: "src/login.go", line: 42, endLine: 88,
    }));
    fireEvent.click(screen.getAllByTestId("transcript-editor-context-file")[1]);
    expect(openSourceLocation).toHaveBeenLastCalledWith(expect.objectContaining({
      rootId: "root-b", path: "guide.md", line: 7, endLine: undefined,
    }));
  });

  it("states a closed editor without listing files", () => {
    render(() => <EditorContextLine editor={{ files: [], more_files: 0 }} projectId="project" />);
    expect(screen.queryAllByTestId("transcript-editor-context-file")).toHaveLength(0);
    expect(screen.getByTestId("transcript-editor-context").textContent).not.toBe("");
  });
});
