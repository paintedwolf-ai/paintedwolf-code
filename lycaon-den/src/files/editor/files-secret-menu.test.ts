// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import {
  filesContextMenuItems,
  type EditorSecretActions,
} from "../commands/project-files-context-menu.ts";

const baseActions = {
  newFile: vi.fn(),
  newFolder: vi.fn(),
  rename: vi.fn(),
  moveTo: vi.fn(),
  duplicate: vi.fn(),
  moveToTrash: vi.fn(),
  copyPath: vi.fn(),
  copyRelativePath: vi.fn(),
  addToChat: vi.fn(),
  collapseAllFolders: vi.fn(),
  expandAllFolders: vi.fn(),
  closeTab: vi.fn(),
  closeOtherTabs: vi.fn(),
  closeToTheRight: vi.fn(),
  closeSavedTabs: vi.fn(),
  closeAllTabs: vi.fn(),
  keepOpenTab: vi.fn(),
  tabIsPreview: vi.fn(() => false),
  togglePinTab: vi.fn(),
  tabIsPinned: vi.fn(() => false),
  openFilesList: vi.fn(),
  revealInTree: vi.fn(),
  tabRevealInTree: () => ({ onSelect: vi.fn() }),
  openFile: vi.fn(),
  editorSymbol: {
    target: { kind: "none" as const },
    rename: vi.fn(),
    goToDefinition: vi.fn(),
  },
};

function editorMenu(editorSecret: EditorSecretActions | undefined) {
  const el = document.createElement("div");
  el.setAttribute("contenteditable", "true");
  return filesContextMenuItems(
    {
      surface: "editor",
      hasSelection: true,
      textEdit: {
        kind: "editable",
        snapshot: { el, start: 0, end: 0, range: null, readOnly: false },
      },
    },
    {
      ...baseActions,
      ...(editorSecret ? { editorSecret } : {}),
    } as never,
  )!;
}

const ids = (items: ReturnType<typeof editorMenu>) => items.map((i) => i.testId);

const detected = (siblings = 0): EditorSecretActions["span"] => ({
  fact: "AWS access key \u00b7 possible credential",
  state: "detected",
  reference: "",
  siblings,
});

const tracked = (siblings = 0): EditorSecretActions["span"] => ({
  fact: "A managed secret \u00b7 managed secret",
  state: "tracked",
  reference: "{{paintedwolf-secret:a}}",
  siblings,
});

describe("editor secret menu block", () => {
  it("offers marking for a plain selection no rule matched", () => {
    const items = editorMenu({ span: null, markSelection: vi.fn() });
    expect(ids(items)).toContain("files-ctx-editor-mark-secret");
    expect(ids(items)).not.toContain("files-ctx-editor-secret-fact");
  });

  it("adds nothing when there is neither a span nor an eligible selection", () => {
    const items = editorMenu({ span: null });
    expect(ids(items)).not.toContain("files-ctx-editor-mark-secret");
  });

  it("adds nothing at all when the editor has no secret facts", () => {
    expect(ids(editorMenu(undefined))).not.toContain("files-ctx-editor-mark-secret");
  });

  it("leads with the host fact when the caret sits in a span", () => {
    const items = editorMenu({
      span: detected(),
      trackSpan: vi.fn(),
      ignoreSpan: vi.fn(),
    });
    const factIndex = items.findIndex((i) => i.testId === "files-ctx-editor-secret-fact");
    const trackIndex = items.findIndex((i) => i.testId === "files-ctx-editor-track-secret");
    expect(factIndex).toBeGreaterThanOrEqual(0);
    expect(trackIndex).toBe(factIndex + 1);
    expect(ids(items)).toContain("files-ctx-editor-ignore-secret");
  });

  it("offers the reference on a tracked span and never offers to ignore it", () => {
    const items = editorMenu({
      span: tracked(),
      copyReference: vi.fn(),
      ignoreSpan: vi.fn(),
      trackSpan: vi.fn(),
    });
    expect(ids(items)).toContain("files-ctx-editor-copy-reference");
    // Protected capabilities cannot become public fixture declarations.
    expect(ids(items)).not.toContain("files-ctx-editor-ignore-secret");
    expect(ids(items)).not.toContain("files-ctx-editor-track-secret");
  });

  it("counts siblings in the label when the value repeats in this buffer", () => {
    const items = editorMenu({ span: detected(3), selectSiblings: vi.fn() });
    const row = items.find((i) => i.testId === "files-ctx-editor-secret-siblings");
    expect(row?.label).toContain("(3)");
  });

  it("omits the sibling row when the value appears once", () => {
    const items = editorMenu({ span: detected(0), selectSiblings: vi.fn() });
    expect(ids(items)).not.toContain("files-ctx-editor-secret-siblings");
  });

  it("stays inside the twelve-row context-menu ceiling", () => {
    const items = editorMenu({
      span: tracked(2),
      copyReference: vi.fn(),
      selectSiblings: vi.fn(),
      openInSecrets: vi.fn(),
      markSelection: vi.fn(),
    });
    expect(items.length).toBeLessThanOrEqual(12);
  });
});
