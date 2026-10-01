import { contextAction } from "../components/context-actions.ts";
import type { ContextMenuItem } from "../components/ContextMenu.tsx";

export function sessionExportItems(opts: {
  disabled: boolean;
  onExport: (format: "md" | "json") => void;
}): ContextMenuItem[] {
  return [
    contextAction("export", {
      testId: "session-menu-export",
      disabled: opts.disabled,
      submenu: [
        contextAction("exportAsMarkdown", {
          testId: "session-menu-export-md",
          onSelect: () => opts.onExport("md"),
        }),
        contextAction("exportAsJSON", {
          testId: "session-menu-export-json",
          onSelect: () => opts.onExport("json"),
        }),
      ],
    }),
  ];
}

export function sessionExportOverflowItems(opts: {
  onExport: (format: "md" | "json") => void;
}): ContextMenuItem[] {
  return [
    contextAction("exportAsMarkdown", {
      testId: "session-export-md",
      onSelect: () => opts.onExport("md"),
    }),
    contextAction("exportAsJSON", {
      testId: "session-export-json",
      onSelect: () => opts.onExport("json"),
    }),
  ];
}

export function messagesHaveExportableTranscript(
  messages: readonly { role: string }[],
): boolean {
  return messages.some((m) => m.role !== "system");
}
