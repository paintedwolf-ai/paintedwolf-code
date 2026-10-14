import { isTauriRuntime } from "../runtime.ts";
import { buildAppMenuSpec, type AppMenuSpec } from "../../shortcuts/app-menu-model.ts";
import { appMenuSurface } from "./app-menu-surface.ts";
import {
  contributionCommand,
  contributionCommandAvailable,
} from "../../contributions/dispatch.ts";
import { listenHostEvent } from "../windows/window-channel.ts";
import {
  shortcutBindingAvailable,
  shortcutCommandsSuspended,
} from "../../shortcuts/dispatcher.ts";

const MENU_COMMAND_EVENT = "menu://command";

let lastPushed: string | null = null;
let appliedSections: AppMenuSpec[] = [];
/** The shell's activation surface, shared by native and in-window menus. */
let activateCommand: ((commandId: string) => void) | null = null;

type HostAppMenuSpec = {
  menu: string;
  title: string;
  groups: Array<
    Array<{
      id: string;
      title: string;
      accelerator: string | null;
      enabled: boolean;
      checked?: boolean;
    }>
  >;
};

type QueuedMenu = {
  sections: AppMenuSpec[];
  hostSections: HostAppMenuSpec[];
  serialized: string;
};

let queued: QueuedMenu | null = null;
let pushing: Promise<void> | null = null;

/** Resets menu state between tests. */
export function resetAppMenuForTests(): void {
  lastPushed = null;
  appliedSections = [];
  queued = null;
  pushing = null;
  activateCommand = null;
}

/** Forces the focused window to publish its menu. */
export function invalidateAppMenuProjection(): void {
  lastPushed = null;
}

function hostMenuSections(sections: AppMenuSpec[]): HostAppMenuSpec[] {
  return sections.map((section) => ({
    menu: section.menu,
    title: section.title,
    groups: section.groups.map((group) =>
      group.map(({ id, title, accelerator, enabled, checked }) => ({
        id,
        title,
        accelerator,
        enabled,
        checked,
      })),
    ),
  }));
}

/** Publishes the newest menu projection. */
export async function syncAppMenu(
  overrides: Record<string, string> | null,
): Promise<void> {
  // An in-window menu reads the spec when it opens; nothing is pushed to the host.
  if (!isTauriRuntime() || appMenuSurface() !== "native") return;
  let sections: AppMenuSpec[];
  try {
    sections = buildAppMenuSpec({ overrides });
  } catch (err) {
    console.debug("[app-menu] build failed", err);
    return;
  }
  const hostSections = hostMenuSections(sections);
  const serialized = JSON.stringify(hostSections);
  if (!pushing && serialized === lastPushed) {
    appliedSections = sections;
    return;
  }
  queued = { sections, hostSections, serialized };
  if (!pushing) {
    pushing = drainAppMenu();
  }
  return pushing;
}

/** Drains coalesced menu projections. */
async function drainAppMenu(): Promise<void> {
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    while (queued) {
      const next = queued;
      queued = null;
      if (next.serialized === lastPushed) {
        appliedSections = next.sections;
        continue;
      }
      const applied = await invoke<boolean>("set_app_menu", {
        sections: next.hostSections,
      });
      if (applied) {
        lastPushed = next.serialized;
        appliedSections = next.sections;
      }
    }
  } catch (err) {
    console.error("[app-menu] set failed — menu bar is the host default", err);
  } finally {
    pushing = null;
  }
}

/**
 * Runs a chosen menu command through the same gate as its shortcut: known,
 * available, not suspended, and with its projected accelerator still live.
 */
export function activateAppMenuCommand(id: string): boolean {
  if (!activateCommand || !contributionCommand(id)) return false;
  if (shortcutCommandsSuspended()) return false;
  if (!contributionCommandAvailable(id)) return false;
  const item = appliedSections
    .flatMap((section) => section.groups.flat())
    .find((row) => row.id === id);
  if (
    item?.accelerator &&
    (!item.bindingId ||
      !item.binding ||
      !shortcutBindingAvailable(item.bindingId, item.binding))
  ) {
    return false;
  }
  activateCommand(id);
  return true;
}

/** Routes menu commands through the Shell's shared activation surface. */
export function attachAppMenuCommands(activate: (commandId: string) => void): () => void {
  activateCommand = activate;
  const detach = () => {
    if (activateCommand === activate) activateCommand = null;
  };
  if (!isTauriRuntime() || appMenuSurface() !== "native") return detach;

  let unlisten: (() => void) | null = null;
  let cancelled = false;

  void (async () => {
    try {
      const stop = await listenHostEvent<string>(MENU_COMMAND_EVENT, (event) => {
        const id = event.payload;
        if (typeof id !== "string") return;
        activateAppMenuCommand(id);
      });
      if (cancelled) stop();
      else unlisten = stop;
    } catch (err) {
      console.debug("[app-menu] listen failed", err);
    }
  })();

  return () => {
    detach();
    cancelled = true;
    if (unlisten) unlisten();
    unlisten = null;
  };
}
