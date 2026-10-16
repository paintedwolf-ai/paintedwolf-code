import { createSignal } from "solid-js";
import { isTauriRuntime } from "../runtime.ts";
import { currentWindow } from "./current-window.ts";

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

/**
 * Follows the window's maximized and fullscreen state through its resizes.
 * The listener goes on before the first read, so a change while the window
 * is first shown (the compositor may maximize it then) is not missed.
 */
export async function trackWindowMaximized(): Promise<void> {
  if (!isTauriRuntime()) return;
  const win = await currentWindow();
  let latest = 0;
  const refresh = async () => {
    const read = ++latest;
    try {
      const [isMaximized, isFullscreen] = await Promise.all([win.isMaximized(), win.isFullscreen()]);
      // A slower earlier read must not overwrite a newer one.
      if (read !== latest) return;
      setMaximized(isMaximized);
      setFullscreen(isFullscreen);
    } catch (err) {
      console.debug("[window-chrome] window state unavailable", err);
    }
  };
  // Maximize, restore, and fullscreen all land as a resize.
  await win.onResized(() => void refresh());
  await refresh();
}
