import { createSignal } from "solid-js";
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";
import { listenHostEvent } from "./window-channel.ts";

/** One control the desktop places in a client-side title bar. */
export type WindowChromeControl = "appmenu" | "minimize" | "maximize" | "close";

/** Which side of the window a control cluster sits on. */
export type WindowChromeSide = "left" | "right";

/** What a click on the title bar does, as the desktop configures it. */
export type TitlebarAction = "toggle-maximize" | "minimize" | "lower" | "menu" | "none";

/** The title bar's double, middle, and right click actions. */
export type TitlebarActions = {
  double_click: TitlebarAction;
  middle_click: TitlebarAction;
  right_click: TitlebarAction;
};

/** GTK's defaults when the desktop states none. */
export const DEFAULT_TITLEBAR_ACTIONS: TitlebarActions = {
  double_click: "toggle-maximize",
  middle_click: "none",
  right_click: "menu",
};

/** Controls on each side of the title bar, in display order, and what clicks on it do. */
export type WindowChromeLayout = {
  left: readonly WindowChromeControl[];
  right: readonly WindowChromeControl[];
  /** `portal` when the desktop published its layout; `default` otherwise. */
  source: "portal" | "default";
  actions: TitlebarActions;
};

const CONTROLS: readonly WindowChromeControl[] = ["appmenu", "minimize", "maximize", "close"];

/** The full set on the right, with the app menu beside it. */
export const DEFAULT_WINDOW_CHROME_LAYOUT: WindowChromeLayout = {
  left: [],
  right: ["appmenu", "minimize", "maximize", "close"],
  source: "default",
  actions: DEFAULT_TITLEBAR_ACTIONS,
};

const ACTIONS: readonly TitlebarAction[] = ["toggle-maximize", "minimize", "lower", "menu", "none"];

function titlebarActions(raw: unknown): TitlebarActions {
  if (!raw || typeof raw !== "object") return DEFAULT_TITLEBAR_ACTIONS;
  const record = raw as Record<string, unknown>;
  const action = (value: unknown, fallback: TitlebarAction): TitlebarAction =>
    typeof value === "string" && (ACTIONS as readonly string[]).includes(value) ? (value as TitlebarAction) : fallback;
  return {
    double_click: action(record.double_click, DEFAULT_TITLEBAR_ACTIONS.double_click),
    middle_click: action(record.middle_click, DEFAULT_TITLEBAR_ACTIONS.middle_click),
    right_click: action(record.right_click, DEFAULT_TITLEBAR_ACTIONS.right_click),
  };
}

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
  const actions = titlebarActions(record.actions);
  if (left.length === 0 && right.length === 0) return { ...DEFAULT_WINDOW_CHROME_LAYOUT, actions };
  if (!left.includes("appmenu") && !right.includes("appmenu")) right.unshift("appmenu");
  return {
    left,
    right,
    source: record.source === "portal" ? "portal" : "default",
    actions,
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

export function setWindowChromeLayoutForTests(
  next: (Omit<WindowChromeLayout, "actions"> & { actions?: TitlebarActions }) | null,
): void {
  setLayout(next ? { actions: DEFAULT_TITLEBAR_ACTIONS, ...next } : DEFAULT_WINDOW_CHROME_LAYOUT);
}

/** The host sends this after the desktop's `button-layout` changes. */
export const WINDOW_CHROME_LAYOUT_CHANGED_EVENT = "linux-decoration-layout-changed";

/**
 * Follows the desktop's button layout: listens for changes before the first
 * read so a change between the two is not lost. The default stands on any
 * failure.
 */
export async function trackWindowChromeLayout(): Promise<void> {
  if (!isTauriRuntime() || !drawsWindowControls()) return;
  try {
    let changed = false;
    await listenHostEvent<unknown>(WINDOW_CHROME_LAYOUT_CHANGED_EVENT, ({ payload }) => {
      changed = true;
      setLayout(normalizeWindowChromeLayout(payload));
    });
    const { invoke } = await import("@tauri-apps/api/core");
    const initial = normalizeWindowChromeLayout(await invoke("linux_decoration_layout"));
    if (!changed) setLayout(initial);
  } catch (err) {
    console.debug("[window-chrome] decoration layout unavailable", err);
  }
}
