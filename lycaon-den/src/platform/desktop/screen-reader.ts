/**
 * Stamps whether the host reports a running screen reader on the root element.
 *
 * WebKitGTK aborts its web process answering a screen reader's text-attribute
 * queries over text that cannot be selected, so `ui-chrome.css` makes all text
 * selectable while the fact holds.
 */

import { isTauriRuntime } from "../runtime.ts";
import { listenHostEvent } from "../windows/window-channel.ts";

export const SCREEN_READER_CHANGED_EVENT = "accessibility://screen-reader-changed";
export const SCREEN_READER_ATTRIBUTE = "data-den-screen-reader";

export type ScreenReaderPayload = {
  active: boolean;
  /** Monotonic version rejects a boot snapshot older than a live change. */
  revision: number;
};

let latestRevision = -1;

export function setScreenReaderActive(active: boolean): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  if (active) root.setAttribute(SCREEN_READER_ATTRIBUTE, "active");
  else root.removeAttribute(SCREEN_READER_ATTRIBUTE);
}

export function applyScreenReaderPayload(payload: ScreenReaderPayload): void {
  if (payload.revision < latestRevision) return;
  latestRevision = payload.revision;
  setScreenReaderActive(payload.active);
}

/** Applies the current host fact and follows changes. */
export async function setupScreenReader(): Promise<void> {
  if (!isTauriRuntime()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await listenHostEvent<ScreenReaderPayload>(SCREEN_READER_CHANGED_EVENT, (event) => {
      applyScreenReaderPayload(event.payload);
    });
    applyScreenReaderPayload(await invoke<ScreenReaderPayload>("accessibility_screen_reader"));
  } catch {
    /* Without the host fact, chrome text keeps its authored selection behavior. */
  }
}

/** Test-only reset. */
export function resetScreenReaderForTests(): void {
  latestRevision = -1;
  setScreenReaderActive(false);
}
