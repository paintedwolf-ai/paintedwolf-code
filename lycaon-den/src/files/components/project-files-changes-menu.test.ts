// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import {
  filesContextMenuItems,
  resolveFilesContextTarget,
  type FilesContextMenuActions,
} from "../commands/project-files-context-menu.ts";

function actions(
  overrides: Partial<FilesContextMenuActions> = {},
): FilesContextMenuActions {
  return {
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
    tabIsPreview: () => false,
    togglePinTab: vi.fn(),
    tabIsPinned: () => false,
    openFilesList: vi.fn(),
    renameTab: vi.fn(),
    trashTab: vi.fn(),
    copyTabPath: vi.fn(),
    copyTabRelativePath: vi.fn(),
    addTabToChat: vi.fn(),
    showTabChanges: vi.fn(),
    openFile: vi.fn(),
    revertChangedFile: vi.fn(),
    copyPathLine: vi.fn(),
    copyRelativePathLine: vi.fn(),
    addPathLineToChat: vi.fn(),
    revealInTree: vi.fn(),
  tabRevealInTree: () => ({ onSelect: vi.fn() }),
    editorSymbol: {
      target: { kind: "missing", reason: "whitespace" },
      rename: vi.fn(),
      goToDefinition: vi.fn(),
    },
    ...overrides,
  };
}

describe("changes context menu rows", () => {
  it("resolves changed-file surfaces", () => {
    const file = document.createElement("button");
    file.dataset.filesCtx = "changed-file";
    file.dataset.rootId = "r1";
    file.dataset.path = "src/a.ts";
    expect(resolveFilesContextTarget(file)).toEqual({
      surface: "changed-file",
      rootId: "r1",
      path: "src/a.ts",
      name: "a.ts",
    });
  });

  it("lists the locked changed-file items and runs revert", () => {
    const revert = vi.fn();
    const items = filesContextMenuItems(
      {
        surface: "changed-file",
        rootId: "r1",
        path: "src/a.ts",
        name: "a.ts",
      },
      actions({ revertChangedFile: revert }),
    );
    expect(items?.map((i) => i.label)).toEqual([
      "Open",
      "Reveal in tree",
      "Copy path",
      "Copy relative path",
      "Add to chat",
      undefined,
      "Restore comparison baseline",
    ]);
    items?.find((i) => i.testId === "files-ctx-changed-revert")?.onSelect!();
    expect(revert).toHaveBeenCalledWith("r1", "src/a.ts");
  });
});
