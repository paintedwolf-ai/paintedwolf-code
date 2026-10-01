import type { ContextMenuItem } from "./ContextMenu.tsx";
import { bindContextAction, contextAction } from "./context-actions.ts";

/** All window families use the same ordering and omit empty destination lists. */
export function windowMenuItems<T>(options: {
  views: readonly { id: T; number: number; title?: string }[];
  testIdPrefix: string;
  open?: () => void;
  focus?: (id: T) => void;
  close?: (id: T) => void;
}): ContextMenuItem[] {
  const items: ContextMenuItem[] = [];
  const prefix = options.testIdPrefix;
  if (options.open) items.push(contextAction(options.views.length ? "openAnotherWindow" : "openInNewWindow", {
    testId: `${prefix}-open-window`, onSelect: options.open,
  }));
  for (const [action, handler] of [["focus", options.focus], ["close", options.close]] as const) {
    if (!handler || !options.views.length) continue;
    items.push(contextAction(action === "focus" ? "focusWindow" : "closeWindow", {
      testId: `${prefix}-${action}-window`, submenu: options.views.map((view) => bindContextAction({
        label: `Window ${view.number}${view.title ? ` — ${view.title}` : ""}`,
        testId: `${prefix}-${action}-view-${view.number}`,
        onSelect: () => handler(view.id),
      })),
    }));
  }
  return items;
}
