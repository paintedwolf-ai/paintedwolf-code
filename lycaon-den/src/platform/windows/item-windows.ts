import { createSignal } from "solid-js";
import { invoke } from "@tauri-apps/api/core";
import { isTauriRuntime } from "../runtime.ts";
import { listenHostEvent } from "./window-channel.ts";

export type OpenItemWindowArgs =
  | {
      kind: "session";
      projectId: string;
      sessionId: string;
      title: string;
      position?: ItemWindowDropPosition;
    }
  | {
      kind: "file";
      projectId: string;
      rootId: string;
      path: string;
      title: string;
      position?: ItemWindowDropPosition;
    }
  | {
      kind: "context";
      projectId: string;
      stageId: string;
      title: string;
      position?: ItemWindowDropPosition;
    };

/** Screen coordinates of the tab drop. */
export type ItemWindowDropPosition = {
  x: number;
  y: number;
};

/** Controls the native window created during a tab drag. */
export type FileTabWindowDrag = {
  begin: (
    bufferKey: string,
    position: ItemWindowDropPosition,
  ) => Promise<string>;
  move: (label: string, position: ItemWindowDropPosition) => Promise<boolean>;
  finish: (
    label: string,
    position: ItemWindowDropPosition,
  ) => Promise<boolean>;
  cancel: (label: string) => Promise<boolean>;
};

export type FileTabWindowDragEvent = {
  phase: "hover" | "leave" | "drop";
  dragLabel: string;
  projectId: string;
  rootId: string;
  path: string;
  title: string;
  positionX: number;
  positionY: number;
};

export type ItemWindowView = {
  label: string;
  title: string;
  /** Host-assigned workspace view slot. Stable for this app lifetime. */
  viewNumber: number;
  kind: "session" | "file" | "context";
  projectId: string;
  sessionId?: string;
  rootId?: string;
  path?: string;
  stageId?: string;
};

const [inventoryReady, setInventoryReady] = createSignal(false);
export const itemWindowInventoryReady = inventoryReady;

const [views, setViews] = createSignal<readonly ItemWindowView[]>([]);
type ViewMirror = { users: number; ready: Promise<void>; stop: () => void };
let viewMirror: ViewMirror | undefined;

/** Live native window registry. */
export function itemWindowViews(): readonly ItemWindowView[] {
  return views();
}

export async function openItemWindow(
  args: OpenItemWindowArgs,
): Promise<void> {
  if (!isTauriRuntime()) throw new Error("Item windows require the desktop shell");
  await invoke("open_item_window", {
    kind: args.kind,
    projectId: args.projectId,
    sessionId: args.kind === "session" ? args.sessionId : null,
    rootId: args.kind === "file" ? args.rootId : null,
    path: args.kind === "file" ? args.path : null,
    stageId: args.kind === "context" ? args.stageId : null,
    title: args.title,
    positionX: args.position?.x ?? null,
    positionY: args.position?.y ?? null,
  });
}

export async function beginFileItemWindowDrag(
  args: Extract<OpenItemWindowArgs, { kind: "file" }> & {
    position: ItemWindowDropPosition;
  },
): Promise<string> {
  if (!isTauriRuntime()) throw new Error("Item windows require the desktop shell");
  return await invoke<string>("begin_file_item_window_drag", {
    projectId: args.projectId,
    rootId: args.rootId,
    path: args.path,
    title: args.title,
    positionX: args.position.x,
    positionY: args.position.y,
  });
}

export async function moveItemWindowDrag(
  label: string,
  position: ItemWindowDropPosition,
): Promise<boolean> {
  return await invoke<boolean>("move_item_window_drag", {
    label,
    positionX: position.x,
    positionY: position.y,
  });
}

export async function finishItemWindowDrag(
  label: string,
  position: ItemWindowDropPosition,
): Promise<boolean> {
  return await invoke<boolean>("finish_item_window_drag", {
    label,
    positionX: position.x,
    positionY: position.y,
  });
}

export async function cancelItemWindowDrag(label: string): Promise<boolean> {
  return await invoke<boolean>("cancel_item_window_drag", { label });
}

/** Reloading cancels pending tab drags and releases their hidden windows. */
export async function discardPendingItemWindowDrags(): Promise<void> {
  if (!isTauriRuntime()) return;
  try {
    await invoke("discard_pending_item_window_drags");
  } catch (err) {
    console.debug("[item-window] discard pending drags failed", err);
  }
}

/** Cached geometry avoids duplicate IPC. */
let publishedDropTarget: string | null = null;

export async function updateFileTabDropTarget(
  projectId: string,
  rect: Pick<DOMRect, "x" | "y" | "width" | "height">,
): Promise<void> {
  if (!isTauriRuntime()) return;
  const published = [projectId, rect.x, rect.y, rect.width, rect.height].join(
    "\u0000",
  );
  if (published === publishedDropTarget) return;
  publishedDropTarget = published;
  await invoke("update_file_tab_drop_target", {
    projectId,
    x: rect.x,
    y: rect.y,
    width: rect.width,
    height: rect.height,
  });
}

export async function clearFileTabDropTarget(): Promise<void> {
  if (!isTauriRuntime()) return;
  if (publishedDropTarget === null) return;
  publishedDropTarget = null;
  await invoke("clear_file_tab_drop_target");
}


export async function closeCurrentPeerWindow(): Promise<boolean> {
  if (!isTauriRuntime()) return false;
  const { getCurrentWebviewWindow } = await import(
    "@tauri-apps/api/webviewWindow"
  );
  await getCurrentWebviewWindow().close();
  return true;
}

export async function listenFileTabWindowDrag(
  handler: (event: FileTabWindowDragEvent) => void,
): Promise<() => void> {
  return await listenHostEvent<FileTabWindowDragEvent>(
    "file-tab-window-drag",
    ({ payload }) => handler(payload),
  );
}

export async function focusItemWindow(label: string): Promise<boolean> {
  if (!isTauriRuntime()) return false;
  try {
    return await invoke<boolean>("focus_item_window", { label });
  } catch {
    return false;
  }
}

export async function closeItemWindow(label: string): Promise<boolean> {
  if (!isTauriRuntime()) return false;
  try {
    return await invoke<boolean>("close_item_window", { label });
  } catch {
    return false;
  }
}

/** Shares one native registry subscription across mounted shells. */
export async function startItemWindowViewMirror(): Promise<() => void> {
  if (!isTauriRuntime()) {
    setViews([]);
    setInventoryReady(true);
    return () => {};
  }
  if (!viewMirror) {
    setInventoryReady(false);
    setViews([]);
    const mirror: ViewMirror = { users: 0, ready: Promise.resolve(), stop: () => {} };
    viewMirror = mirror;
    mirror.ready = (async () => {
      let receivedEvent = false;
      try {
        mirror.stop = await listenHostEvent<ItemWindowView[]>(
          "item-window-views-changed",
          (event) => {
            if (viewMirror !== mirror) return;
            receivedEvent = true;
            setViews(event.payload);
            setInventoryReady(true);
          },
        );
        const initial = await invoke<ItemWindowView[]>("list_item_window_views");
        if (viewMirror === mirror && !receivedEvent) { setViews(initial); setInventoryReady(true); }
      } catch (error) {
        if (viewMirror === mirror) viewMirror = undefined;
        mirror.stop();
        throw error;
      }
    })();
  }
  const mirror = viewMirror;
  mirror.users++;
  await mirror.ready;
  let stopped = false;
  return () => {
    if (stopped) return;
    stopped = true;
    if (--mirror.users !== 0) return;
    if (viewMirror === mirror) { viewMirror = undefined; setInventoryReady(false); }
    mirror.stop();
  };
}
