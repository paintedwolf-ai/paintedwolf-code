import { render } from "@solidjs/testing-library";
import axe from "axe-core";
import { describe, expect, it } from "vitest";
import { ContextMenu } from "../../components/ContextMenu.tsx";
import { filesContextMenuItems } from "../commands/project-files-context-menu.ts";

describe("files editor context menu a11y", () => {
  it("axe is green on the editor selection menu", async () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    el.textContent = "selection";
    document.body.appendChild(el);
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 9,
            range: null,
            readOnly: false,
          },
        },
      },
      {
        newFile: () => {},
        newFolder: () => {},
        rename: () => {},
        moveTo: () => {},
        duplicate: () => {},
        moveToTrash: () => {},
        copyPath: () => {},
        copyRelativePath: () => {},
        addToChat: () => {},
        collapseAllFolders: () => {},
        expandAllFolders: () => {},
        closeTab: () => {},
        closeOtherTabs: () => {},
        closeToTheRight: () => {},
        closeSavedTabs: () => {},
        closeAllTabs: () => {},
        keepOpenTab: () => {},
        tabIsPreview: () => false,
        togglePinTab: () => {},
        tabIsPinned: () => false,
        openFilesList: () => {},
        renameTab: () => {},
        trashTab: () => {},
        copyTabPath: () => {},
        copyTabRelativePath: () => {},
        addTabToChat: () => {},
        copyPathLine: () => {},
        copyRelativePathLine: () => {},
        addPathLineToChat: () => {},
        revealInTree: () => {},
        tabRevealInTree: () => ({ onSelect: () => {} }),
        editorCopyPath: () => {},
        editorCopyRelativePath: () => {},
        editorAddSelectionToChat: () => {},
        editorSymbol: {
          target: {
            kind: "symbol",
            symbol: "resolveProjectRoot",
            from: 10,
            to: 28,
            source: "pointer",
          },
          rename: () => {},
          goToDefinition: () => {},
        },
        editorSelectionVerb: () => {},
        addSelectionShortcutHint: "⌘L",
      },
    )!;

    const { container, unmount } = render(() => (
      <ContextMenu
        anchor={{ x: 40, y: 40 }}
        items={items}
        onDismiss={() => {}}
      />
    ));
    const results = await axe.run(container, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] },
    });
    const summary = results.violations
      .map(
        (v) =>
          `${v.id}: ${v.help} — ${v.nodes.map((n) => n.target.join(" ")).join("; ")}`,
      )
      .join("\n");
    expect(results.violations, summary || "axe violations").toEqual([]);
    unmount();
    el.remove();
  });
});
