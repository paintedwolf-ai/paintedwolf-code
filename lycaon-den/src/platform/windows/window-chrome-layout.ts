import { createSignal } from "solid-js";
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";

/** One control the desktop places in a client-side title bar. */
export type WindowChromeControl = "appmenu" | "minimize" | "maximize" | "close";

/** Which side of the window a control cluster sits on. */
export type WindowChromeSide = "left" | "right";

/** Controls on each side of the title bar, in display order. */
export type WindowChromeLayout = {
  left: readonly WindowChromeControl[];
  right: readonly WindowChromeControl[];
  /** `portal` when the desktop published its layout; `default` otherwise. */
  source: "portal" | "default";
};

const CONTROLS: readonly WindowChromeControl[] = ["appmenu", "minimize", "maximize", "close"];

/** The full set on the right, with the app menu beside it. */
export const DEFAULT_WINDOW_CHROME_LAYOUT: WindowChromeLayout = {
  left: [],
  right: ["appmenu", "minimize", "maximize", "close"],
  source: "default",
};

function isControl(value: unknown): value is WindowChromeControl {
  return typeof value === "string" && (CONTROLS as readonly string[]).includes(value);
}

/**
 * Validates a layout fact from the host. Unknown tokens are dropped; a layout
 * that never places the app menu gets it at the start of the right cluster, the
 * place a header bar keeps its menu button.
 */
export function normalizeWindowChromeLayout(raw: unknown): WindowChromeLayout {
  if (!raw || typeof raw !== "object") return DEFAULT_WINDOW_CHROME_LAYOUT;
  const record = raw as Record<string, unknown>;
  const side = (value: unknown): WindowChromeControl[] =>
    Array.isArray(value) ? value.filter(isControl) : [];
  const left = side(record.left);
  const right = side(record.right);
  if (left.length === 0 && right.length === 0) return DEFAULT_WINDOW_CHROME_LAYOUT;
  if (!left.includes("appmenu") && !right.includes("appmenu")) right.unshift("appmenu");
  return {
    left,
    right,
    source: record.source === "portal" ? "portal" : "default",
  };
}

const [layout, setLayout] = createSignal<WindowChromeLayout>(DEFAULT_WINDOW_CHROME_LAYOUT);

/** The desktop's title bar layout; the default until the host answers. */
export const windowChromeLayout = layout;

/** Controls the shell draws at one window side. */
export function windowChromeControls(side: WindowChromeSide): readonly WindowChromeControl[] {
  return side === "left" ? layout().left : layout().right;
}

/** Linux draws its own window buttons; other targets keep native ones. */
export function drawsWindowControls(): boolean {
  return tauriPlatform() === "linux";
}

export function setWindowChromeLayoutForTests(next: WindowChromeLayout | null): void {
  setLayout(next ?? DEFAULT_WINDOW_CHROME_LAYOUT);
}

/** Reads the desktop's button layout once; the default stands on any failure. */
export async function loadWindowChromeLayout(): Promise<void> {
  if (!isTauriRuntime() || !drawsWindowControls()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    setLayout(normalizeWindowChromeLayout(await invoke("linux_decoration_layout")));
  } catch (err) {
    console.debug("[window-chrome] decoration layout unavailable", err);
  }
}
