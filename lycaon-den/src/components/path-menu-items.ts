import { contextAction, type ContextActionHandler } from "./context-actions.ts";
import type { ContextMenuItem } from "./ContextMenu.tsx";
import { copyPathMenuItems, type CopyPathMenuOptions } from "./copy-path-menu-items.ts";
import { openInMenuItems } from "./open-in-menu-items.ts";
import type { LocalPathTarget } from "../platform/navigation/open-local-path.ts";

type PathMenuAction = { testId: string; shortcut?: string } & (
  | { onSelect: ContextActionHandler }
  | { disabled: true; description: string }
);

export function pathMenuItems(options: CopyPathMenuOptions & {
  open?: PathMenuAction;
  openIn?: LocalPathTarget | null;
  revealInTree?: PathMenuAction;
  addToChat?: PathMenuAction;
}): ContextMenuItem[] {
  return [
    ...(options.open ? [contextAction("open", options.open)] : []),
    ...(options.revealInTree
      ? [contextAction("revealInTree", options.revealInTree)]
      : []),
    ...openInMenuItems(options.openIn),
    ...copyPathMenuItems(options),
    ...(options.addToChat
      ? [contextAction("addToChat", options.addToChat)]
      : []),
  ];
}
