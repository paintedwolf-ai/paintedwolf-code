import { contextAction } from "../components/context-actions.ts";
import type { ContextMenuItem } from "../components/ContextMenu.tsx";
import { sessionExportItems } from "./session-export-menu.ts";

export function sessionLifecycleMenuItems(options: {
  archived: boolean;
  exportDisabled: boolean;
  onExport: (format: "md" | "json") => void;
  onToggleArchive: () => void;
  onDelete: () => void;
  testIdPrefix: string;
}): ContextMenuItem[] {
  return [
    ...sessionExportItems({ disabled: options.exportDisabled, onExport: options.onExport }),
    contextAction(options.archived ? "unarchiveChat" : "archiveChat", {
      testId: `${options.testIdPrefix}-${options.archived ? "unarchive" : "archive"}`,
      onSelect: options.onToggleArchive,
    }),
    contextAction("deleteChat", {
      testId: `${options.testIdPrefix}-delete`,

      onSelect: options.onDelete,
    }),
  ];
}
