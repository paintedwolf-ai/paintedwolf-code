/**
 * WebKitGTK aborts its web process answering AT-SPI text queries over some
 * content shapes, and the screen reader user loses the window. While the shell
 * reports a WebKitGTK engine, Den renders crash-safe alternatives for those
 * shapes; every other engine keeps its own rendering. Each mechanism asks here
 * and is listed in `webkit-atspi-workaround-inventory.json`.
 */

import { createSignal } from "solid-js";
import { isTauriRuntime } from "../runtime.ts";

export type WebEngineFact = { engine: string; version: string | null };

/** Root attribute the stylesheet alternatives key on. */
export const WEBKIT_ATSPI_WORKAROUND_ATTRIBUTE = "data-den-webkit-atspi-workaround";

const [engineFact, setEngineFact] = createSignal<WebEngineFact | null>(null);

/**
 * Active on WebKitGTK releases without the upstream AT-SPI fix. No release
 * carries it yet, so every reported version qualifies; a version bound on
 * `fact.version` turns the workaround off once one does.
 */
function appliesTo(fact: WebEngineFact | null): boolean {
  return fact?.engine === "webkitgtk";
}

export function webkitAtspiWorkaroundActive(): boolean {
  return appliesTo(engineFact());
}

/** Records the shell's engine fact and stamps the root for the stylesheet. */
export function applyWebEngineFact(fact: WebEngineFact | null): void {
  setEngineFact(fact);
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  if (appliesTo(fact)) root.setAttribute(WEBKIT_ATSPI_WORKAROUND_ATTRIBUTE, "");
  else root.removeAttribute(WEBKIT_ATSPI_WORKAROUND_ATTRIBUTE);
}

/** Reads the engine the shell renders with; without a shell no workaround applies. */
export async function setupWebKitAtspiWorkaround(): Promise<void> {
  if (!isTauriRuntime()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    applyWebEngineFact(await invoke<WebEngineFact>("den_web_engine"));
  } catch {
    /* Without the fact, the default rendering stays. */
  }
}
