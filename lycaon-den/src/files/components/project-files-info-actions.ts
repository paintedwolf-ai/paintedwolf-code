import type { FileTreeRevealAction } from "../tree/files-tree-reveal.ts";
import type { ContextMenuItem } from "../../components/ContextMenu.tsx";
import { pathMenuItems } from "../../components/path-menu-items.ts";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";

type FilePathActionHandlers = {
  openIn?: LocalPathTarget | null;
  revealInTree?: FileTreeRevealAction;
  copyPath: () => void;
  copyRelativePath?: (() => void) | null;
  addToChat: () => void;
};

export function filePathActionItems(
  handlers: FilePathActionHandlers,
  testIdPrefix: string,
): ContextMenuItem[] {
  return pathMenuItems({
    revealInTree: handlers.revealInTree ? { testId: `${testIdPrefix}-reveal-tree`, ...handlers.revealInTree } : undefined,
    openIn: handlers.openIn,
    copyAbsolute: handlers.copyPath,
    copyRelative: handlers.copyRelativePath,
    absoluteTestId: `${testIdPrefix}-copy-path`,
    relativeTestId: `${testIdPrefix}-copy-relative-path`,
    addToChat: { testId: `${testIdPrefix}-add-to-chat`, onSelect: handlers.addToChat },
  });
}
