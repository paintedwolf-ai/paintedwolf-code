import { bindContextAction, contextAction } from "./context-actions.ts";
import type { ContextMenuItem } from "./ContextMenu.tsx";
import { localPathOnDevice, localPathDestinations, localPathDestinationLabel, localPathDestinationUnavailable, openLocalPath, type LocalPathTarget } from "../platform/navigation/open-local-path.ts";

/** Shared by context menus, overflow menus, and explicit destination buttons. */
export function openInDestinationItems(target: LocalPathTarget): NonNullable<ContextMenuItem["submenu"]> {
  return localPathDestinations(target).map((destination) => {
    const description = localPathDestinationUnavailable(target, destination);
    return bindContextAction({
      label: localPathDestinationLabel(destination),
      testId: `open-in-${destination}`,
      ...(description ? { disabled: true as const, description } : {
        description: destination === "file-manager" ? "Reveal this item in its folder"
          : target.entryKind === "file" ? "Open the current file on disk" : "Open this folder in the editor",
        onSelect: () => openLocalPath(target, destination),
      }),
    });
  });
}

export function openInMenuItems(target?: LocalPathTarget | null): ContextMenuItem[] {
  if (!target || !localPathOnDevice(target)) return [];
  return [contextAction("openIn", { testId: "open-in-menu", submenu: openInDestinationItems(target) })];
}
