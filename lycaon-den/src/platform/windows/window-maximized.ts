import { createSignal } from "solid-js";
import { isTauriRuntime } from "../runtime.ts";

const [maximized, setMaximized] = createSignal(false);
const [fullscreen, setFullscreen] = createSignal(false);

/** True while this window is maximized; drawn controls label Restore. */
export const windowMaximized = maximized;

/** True while this window is fullscreen; edge resize zones stand down. */
export const windowFullscreen = fullscreen;

export function setWindowMaximizedForTests(next: boolean): void {
  setMaximized(next);
}

export function setWindowFullscreenForTests(next: boolean): void {
  setFullscreen(next);
}

/** Follows the window's maximized and fullscreen state through its resizes. */
export async function trackWindowMaximized(): Promise<void> {
  if (!isTauriRuntime()) return;
  const { getCurrentWindow } = await import("@tauri-apps/api/window");
  const win = getCurrentWindow();
  const refresh = async () => {
    try {
      const [isMaximized, isFullscreen] = await Promise.all([win.isMaximized(), win.isFullscreen()]);
      setMaximized(isMaximized);
      setFullscreen(isFullscreen);
    } catch (err) {
      console.debug("[window-chrome] window state unavailable", err);
    }
  };
  await refresh();
  // Maximize, restore, and fullscreen all land as a resize.
  await win.onResized(() => void refresh());
}
