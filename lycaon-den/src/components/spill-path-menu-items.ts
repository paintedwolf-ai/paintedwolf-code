import { contextAction } from "./context-actions.ts";
import type { ContextMenuItem } from "./ContextMenu.tsx";
import { copyTextToClipboard } from "../utils/clipboard.ts";
import { addSelectedTextToChat } from "../chat/composer/add-to-chat.ts";
import type { SelectionProvenance } from "../chat/composer/selection-provenance.ts";
import type { ChatDestination } from "../chat/composer/shared-composer-document.ts";

export type SpillPathMenuItemsOptions = {
  /** Host-data-relative spill path as shown on the wire. */
  spillPath: string;
  projectId?: string;
  sessionId?: string;
  toolCallId?: string;
  /** Absent asks which chat receives the path. */
  chatDestination?: ChatDestination;
};

/** Spill paths address host data, so their menu omits filesystem actions. */
export function spillPathMenuItems(
  opts: SpillPathMenuItemsOptions,
): ContextMenuItem[] {
  const path = opts.spillPath.trim();
  if (!path) return [];
  const projectId = opts.projectId?.trim() ?? "";
  const sessionId = opts.sessionId?.trim() ?? "";

  const items: ContextMenuItem[] = [
    contextAction("copyPath", {
      testId: "spill-path-menu-copy",
      onSelect: () => {
        return copyTextToClipboard(path);
      },
    }),
  ];
  if (projectId && sessionId) {
    items.push(contextAction("addToChat", {
      testId: "spill-path-menu-add-to-chat",
      onSelect: () => {
        const prov: SelectionProvenance = {
          text: path,
          path,
          projectId,
          sessionId,
          toolCallId: opts.toolCallId?.trim() || undefined,
        };
        // Spill references attach as text with provenance.
        return addSelectedTextToChat(
          path,
          null,
          prov,
          { destination: opts.chatDestination },
        );
      },
    }));
  }
  return items;
}
