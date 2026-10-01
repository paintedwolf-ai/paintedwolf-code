import type { ContributionFrameResponse } from "../api/types.ts";
import type { CommandScope } from "./keymap.ts";

export const COMMAND_SCOPE_ORDER: readonly CommandScope[] = [
  "global",
  "files",
  "composer",
  "overlay",
];

export const COMMAND_SCOPE_LABEL: Record<CommandScope, string> = {
  global: "Global",
  files: "Files",
  composer: "Composer",
  overlay: "Overlays",
};

export const COMMAND_GROUP_ORDER: readonly string[] = [
  "editing",
  "navigation",
  "buffers",
  "ai",
];

const COMMAND_GROUP_LABEL: Record<string, string> = {
  editing: "Editing",
  navigation: "Navigation",
  buffers: "Buffers",
  ai: "AI",
};

export function commandGroupLabel(group: string): string {
  return COMMAND_GROUP_LABEL[group] ?? group;
}

export const NAV_LEADER_DESCRIPTOR = {
  id: "nav.leader",
  title: "Navigation leader",
} as const;

export type ShortcutDisplayEntry = {
  /** Keybinding declaration ID — the preference and dispatch identity. */
  id: string;
  commandId: string;
  title: string;
  provider: string;
  scope: CommandScope;
  group?: string;
};

export type CommandDisplaySection = {
  id: string;
  label: string;
  scope: CommandScope;
  group?: string;
  entries: ShortcutDisplayEntry[];
};

function commandScope(value: string): CommandScope | null {
  return (COMMAND_SCOPE_ORDER as readonly string[]).includes(value)
    ? (value as CommandScope)
    : null;
}

/** One Settings/help row per keybinding declaration, including inactive ones. */
export function shortcutDisplayEntries(
  frame: ContributionFrameResponse | null,
): ShortcutDisplayEntry[] {
  if (!frame) return [];
  const commands = new Map(frame.commands.map((command) => [command.id, command]));
  const entries: ShortcutDisplayEntry[] = [];
  for (const binding of frame.keybindings) {
    const command = commands.get(binding.command);
    const scope = commandScope(binding.scope);
    if (!command || !scope) continue;
    entries.push({
      id: binding.id,
      commandId: command.id,
      title: command.title,
      provider: command.provider,
      scope,
      ...(command.category ? { group: command.category } : {}),
    });
  }
  return entries;
}

/** Group declaration rows by their own scope and command presentation category. */
export function commandDisplaySections(
  entries: readonly ShortcutDisplayEntry[],
): CommandDisplaySection[] {
  const out: CommandDisplaySection[] = [];
  for (const scope of COMMAND_SCOPE_ORDER) {
    const inScope = entries.filter((entry) => entry.scope === scope);
    if (inScope.length === 0) continue;
    for (const group of COMMAND_GROUP_ORDER) {
      const members = inScope.filter((entry) => entry.group === group);
      if (members.length === 0) continue;
      out.push({
        id: `${scope}-${group}`,
        label: commandGroupLabel(group),
        scope,
        group,
        entries: members,
      });
    }
    const ungrouped = inScope.filter(
      (entry) => !entry.group || !COMMAND_GROUP_ORDER.includes(entry.group),
    );
    if (ungrouped.length > 0) {
      out.push({
        id: scope,
        label: COMMAND_SCOPE_LABEL[scope],
        scope,
        entries: ungrouped,
      });
    }
  }
  return out;
}
