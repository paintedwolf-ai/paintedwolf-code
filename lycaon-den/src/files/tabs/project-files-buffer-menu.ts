import type { FileTreeRevealAction } from "../tree/files-tree-reveal.ts";
import { contextAction } from "../../components/context-actions.ts";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";
import type { ContextMenuItem } from "../../components/ContextMenu.tsx";
import { pathMenuItems } from "../../components/path-menu-items.ts";

export type BufferTabMenuActions = {
  close: () => void;
  closeOthers: () => void;
  closeToTheRight: () => void;
  closeSaved: () => void;
  closeAll: () => void;
  keepOpen?: () => void;
  showKeepOpen?: boolean;
  pin: () => void;
  pinned: boolean;
  copyPath: () => void;
  copyRelativePath: () => void;
  openIn?: LocalPathTarget | null;
  revealInTree?: FileTreeRevealAction;
  addToChat: () => void;
  showChanges?: () => void;
  rename: () => void;
  moveToTrash: () => void;
  openInNewWindow?: () => void;
};

export function bufferTabMenuItems(
  actions: BufferTabMenuActions,
): ContextMenuItem[] {
  const items: ContextMenuItem[] = [
    contextAction("close", {
      testId: "files-ctx-tab-close-tabs",
      submenu: [
        contextAction("closeCurrent", {
          testId: "files-ctx-tab-close",
          onSelect: actions.close,
        }),
        contextAction("closeOthers", {
          testId: "files-ctx-tab-close-others",
          onSelect: actions.closeOthers,
        }),
        contextAction("closeToTheRight", {
          testId: "files-ctx-tab-close-right",
          onSelect: actions.closeToTheRight,
        }),
        contextAction("closeSaved", {
          testId: "files-ctx-tab-close-saved",
          onSelect: actions.closeSaved,
        }),
        contextAction("closeAll", {
          testId: "files-ctx-tab-close-all",
          onSelect: actions.closeAll,
        }),
      ],
    }),
  ];
  if (actions.showKeepOpen && actions.keepOpen) {
    items.push(contextAction("keepOpen", {
      testId: "files-ctx-tab-keep-open",
      onSelect: actions.keepOpen,
    }));
  }
  if (actions.openInNewWindow) {
    items.push(contextAction("openInNewWindow", {
      testId: "files-ctx-tab-open-window",
      onSelect: actions.openInNewWindow,
    }));
  }
  items.push(
    contextAction(actions.pinned ? "unpin" : "pin", {
      testId: actions.pinned ? "files-ctx-tab-unpin" : "files-ctx-tab-pin",
      onSelect: actions.pin,
    }),
    ...pathMenuItems({
      revealInTree: actions.revealInTree ? { testId: "files-ctx-tab-reveal-tree", ...actions.revealInTree } : undefined,
      openIn: actions.openIn,
      copyAbsolute: actions.copyPath,
      copyRelative: actions.copyRelativePath,
      absoluteTestId: "files-ctx-tab-copy-path",
      relativeTestId: "files-ctx-tab-copy-relative-path",
      addToChat: { testId: "files-ctx-tab-add-to-chat", onSelect: actions.addToChat },
    }),
    ...(actions.showChanges
      ? [
          contextAction("showInReview", {
            testId: "files-ctx-tab-show-changes",
            onSelect: actions.showChanges,
          }) satisfies ContextMenuItem,
        ]
      : []),
    contextAction("rename", {
      testId: "files-ctx-tab-rename",
      onSelect: actions.rename,
    }),
    contextAction("moveToTrash", {
      testId: "files-ctx-tab-trash",

      onSelect: actions.moveToTrash,
    }),
  );
  return items;
}

export function tabStripMenuItems(actions: {
  openFilesList: () => void;
  closeSaved: () => void;
  closeAll: () => void;
  openInNewWindow?: () => void;
}): ContextMenuItem[] {
  return [
    contextAction("openFiles", {
      testId: "files-ctx-strip-open-files",
      onSelect: actions.openFilesList,
    }),
    ...(actions.openInNewWindow
      ? [
          contextAction("openInNewWindow", {
            testId: "files-ctx-strip-open-window",
            onSelect: actions.openInNewWindow,
          }) satisfies ContextMenuItem,
        ]
      : []),
    contextAction("closeSaved", {
      testId: "files-ctx-strip-close-saved",
      onSelect: actions.closeSaved,
    }),
    contextAction("closeAll", {
      testId: "files-ctx-strip-close-all",
      onSelect: actions.closeAll,
    }),
  ];
}
