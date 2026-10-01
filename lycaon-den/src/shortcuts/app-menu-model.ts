import { resolvedFrameKeymap } from "../contributions/frame-keymap.ts";
import { contributionFrame } from "../contributions/contribution-store.ts";
import type { ContributionFrameResponse } from "../api/types.ts";
import {
  contributionCommandAvailable,
  contributionCommandRelevant,
} from "../contributions/dispatch.ts";
import { evaluateCondition } from "../contributions/conditions.ts";
import { liveShellFactLookup } from "../contributions/shell-facts.ts";

import {
  acceleratorChord,
  shortcutPlatform,
} from "./platform.ts";
import type { TauriPlatform } from "../platform/runtime.ts";

/** Menu bars, in bar order. Slots are `app_menu.<id>`. */
const APP_MENU_ORDER = [
  "app",
  "file",
  "edit",
  "selection",
  "view",
  "go",
  "window",
  "help",
] as const;

export type AppMenuId = (typeof APP_MENU_ORDER)[number];

const APP_MENU_TITLE: Record<AppMenuId, string> = {
  app: "Painted Wolf Code",
  file: "File",
  edit: "Edit",
  selection: "Selection",
  view: "View",
  go: "Go",
  window: "Window",
  help: "Help",
};

export type AppMenuItemSpec = {
  id: string;
  title: string;
  accelerator: string | null;
  bindingId: string | null;
  binding: string | null;
  enabled: boolean;
  checked?: boolean;
};

export type AppMenuSpec = {
  menu: AppMenuId;
  title: string;
  groups: AppMenuItemSpec[][];
};

type MenuAccelerator = { accelerator: string; bindingId: string; binding: string };

/** Accelerator per command, or null when several declarations compete. */
type MenuAccelerators = Map<string, MenuAccelerator | null>;

/** Chord parsing is the costly part of a menu rebuild; it depends only on these inputs. */
let cachedMenuAccelerators:
  | {
      frame: ContributionFrameResponse | null;
      platform: TauriPlatform;
      overrides: Record<string, string> | null;
      value: MenuAccelerators;
    }
  | undefined;

function menuAccelerators(
  frame: ContributionFrameResponse | null,
  platform: TauriPlatform,
  overrides: Record<string, string> | null,
): MenuAccelerators {
  const cached = cachedMenuAccelerators;
  if (cached && cached.frame === frame && cached.platform === platform && cached.overrides === overrides) {
    return cached.value;
  }
  const keymap = resolvedFrameKeymap(frame, platform, overrides);
  const candidates = new Map<string, MenuAccelerator[]>();
  for (const row of keymap.bindings) {
    const accelerator = acceleratorChord(row.chord);
    if (!accelerator) continue;
    const list = candidates.get(row.commandId) ?? [];
    list.push({ accelerator, bindingId: row.bindingId, binding: row.chord });
    candidates.set(row.commandId, list);
  }
  const value: MenuAccelerators = new Map();
  for (const [commandId, list] of candidates) {
    const declarations = new Set(list.map((candidate) => candidate.bindingId));
    value.set(commandId, declarations.size === 1 ? list[0] ?? null : null);
  }
  cachedMenuAccelerators = { frame, platform, overrides, value };
  return value;
}

/** Projects the active contribution frame into menu sections. */
export function buildAppMenuSpec(opts?: {
  platform?: TauriPlatform;
  overrides?: Record<string, string> | null;
}): AppMenuSpec[] {
  const platform = opts?.platform ?? shortcutPlatform();
  const overrides = opts?.overrides ?? null;
  const frame = contributionFrame();
  const accelerators = menuAccelerators(frame, platform, overrides);
  const lookup = liveShellFactLookup();

  const specs: AppMenuSpec[] = [];
  for (const menu of APP_MENU_ORDER) {
    const slot = `app_menu.${menu}`;
    const placements = (frame?.menus ?? [])
      .filter((m) => m.slot === slot)
      .filter((m) => (lookup ? evaluateCondition(m.when ?? null, lookup) : !m.when))
      .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
    const byGroup = new Map<string, AppMenuItemSpec[]>();
    for (const placement of placements) {
      const command = frame?.commands.find((c) => c.id === placement.command);
      if (!command || !contributionCommandRelevant(command)) continue;
      const accelerator = accelerators.get(command.id) ?? null;
      const state = placement.state
        ? lookup !== null && evaluateCondition(placement.state, lookup)
        : null;
      const item: AppMenuItemSpec = {
        id: command.id,
        title:
          state && placement.state_label
            ? placement.state_label
            : placement.label || command.title,
        accelerator: accelerator?.accelerator ?? null,
        bindingId: accelerator?.bindingId ?? null,
        binding: accelerator?.binding ?? null,
        enabled: contributionCommandAvailable(command.id),
      };
      if (state !== null && !placement.state_label) item.checked = state;
      const key = placement.group ?? "";
      byGroup.set(key, [...(byGroup.get(key) ?? []), item]);
    }
    const groups = [...byGroup.keys()]
      .sort()
      .map((key) => byGroup.get(key) ?? [])
      .filter((group) => group.length > 0);
    specs.push({ menu, title: APP_MENU_TITLE[menu], groups });
  }
  return specs;
}
