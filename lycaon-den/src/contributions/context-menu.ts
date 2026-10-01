import { bindContextAction } from "../components/context-actions.ts";
import type { ContextMenuItem } from "../components/ContextMenu.tsx";
import { contributionFrame } from "./contribution-store.ts";
import { evaluateCondition } from "./conditions.ts";
import { liveShellFactLookup } from "./shell-facts.ts";
import { contributionCommandAvailable, contributionCommandRelevant } from "./dispatch.ts";
import { invokeHostCommand } from "../shortcuts/dispatcher.ts";

/** Menu placements resolve through the shared command registry. */
export function contributionContextMenuItems(slots: readonly string[]): ContextMenuItem[] {
  const frame = contributionFrame();
  const lookup = liveShellFactLookup();
  if (!frame || !lookup) return [];
  const items: ContextMenuItem[] = [];
  let previousGroup: string | null = null;
  const placements = frame.menus.filter((menu) => slots.includes(menu.slot))
    .sort((a, b) => slots.indexOf(a.slot) - slots.indexOf(b.slot) ||
      (a.group ?? "").localeCompare(b.group ?? "") || (a.order ?? 0) - (b.order ?? 0));
  for (const menu of placements) {
    const command = frame.commands.find((row) => row.id === menu.command);
    if (!command || !contributionCommandRelevant(command) || !evaluateCondition(menu.when ?? null, lookup)) continue;
    const group = `${menu.slot}\0${menu.group ?? ""}`;
    if (previousGroup != null && previousGroup !== group) items.push({ separator: true });
    previousGroup = group;
    const state = menu.state ? evaluateCondition(menu.state, lookup) : null;
    items.push(bindContextAction({
      label: state && menu.state_label ? menu.state_label : menu.label || command.title,
      ...(state != null && !menu.state_label ? { checked: state } : {}),
      onSelect: () => invokeHostCommand(command.id),
    }, () => contributionCommandAvailable(command.id)));
  }
  return items;
}
