import { isTauriRuntime } from "../runtime.ts";
import { buildAppMenuSpec, type AppMenuSpec } from "../../shortcuts/app-menu-model.ts";
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
  if (!isTauriRuntime()) return;
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

/** Routes menu commands through the Shell's shared activation surface. */
export function attachAppMenuCommands(activate: (commandId: string) => void): () => void {
  if (!isTauriRuntime()) return () => undefined;

  let unlisten: (() => void) | null = null;
  let cancelled = false;

  void (async () => {
    try {
      const stop = await listenHostEvent<string>(MENU_COMMAND_EVENT, (event) => {
        const id = event.payload;
        if (typeof id !== "string" || !contributionCommand(id)) return;
        if (shortcutCommandsSuspended()) return;
        if (!contributionCommandAvailable(id)) return;
        const item = appliedSections
          .flatMap((section) => section.groups.flat())
          .find((row) => row.id === id);
        if (
          item?.accelerator &&
          (!item.bindingId ||
            !item.binding ||
            !shortcutBindingAvailable(item.bindingId, item.binding))
        ) {
          return;
        }
        activate(id);
      });
      if (cancelled) stop();
      else unlisten = stop;
    } catch (err) {
      console.debug("[app-menu] listen failed", err);
    }
  })();

  return () => {
    cancelled = true;
    if (unlisten) unlisten();
    unlisten = null;
  };
}
