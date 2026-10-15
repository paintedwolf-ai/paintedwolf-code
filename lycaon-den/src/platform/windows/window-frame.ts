import { isTauriRuntime, tauriPlatform } from "../runtime.ts";
import { currentWindow } from "./current-window.ts";

/** How a Linux window sits on the desktop, as the host reports it. */
export type WindowFrame = {
  /** Not maximized, fullscreen, or tiled: the frame shows corners and shadow. */
  floating: boolean;
  /** Content size in logical pixels, without the frame's shadow. */
  content_width: number;
  content_height: number;
};

/** The host sends this to a window when it starts or stops floating. */
export const WINDOW_FRAME_CHANGED_EVENT = "linux-window-frame-changed";

/** Root attribute the stylesheet rounds the page from. */
export const WINDOW_FRAME_ATTR = "data-window-frame";

function publish(floating: boolean): void {
  document.documentElement.setAttribute(WINDOW_FRAME_ATTR, floating ? "floating" : "fitted");
}

function isFrame(value: unknown): value is WindowFrame {
  if (!value || typeof value !== "object") return false;
  const frame = value as Record<string, unknown>;
  return (
    typeof frame.floating === "boolean" &&
    typeof frame.content_width === "number" &&
    typeof frame.content_height === "number"
  );
}

/** The window's frame from the host; null off Linux or when it cannot answer. */
export async function readWindowFrame(): Promise<WindowFrame | null> {
  if (!isTauriRuntime() || tauriPlatform() !== "linux") return null;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const frame = await invoke<unknown>("linux_window_frame");
    return isFrame(frame) ? frame : null;
  } catch (err) {
    console.debug("[window-chrome] window frame unavailable", err);
    return null;
  }
}

/**
 * Follows whether this Linux window floats. The page squares its corners
 * until the host says otherwise, since a fitted window has no frame to match.
 */
export async function trackWindowFrame(): Promise<void> {
  if (!isTauriRuntime() || tauriPlatform() !== "linux") return;
  publish(false);
  let changed = false;
  try {
    const win = await currentWindow();
    await win.listen<unknown>(WINDOW_FRAME_CHANGED_EVENT, ({ payload }) => {
      if (!isFrame(payload)) return;
      changed = true;
      publish(payload.floating);
    });
  } catch (err) {
    console.debug("[window-chrome] window frame changes unavailable", err);
  }
  const frame = await readWindowFrame();
  if (frame && !changed) publish(frame.floating);
}
