import type { JSX } from "solid-js";
import { tauriPlatform } from "../../platform/runtime.ts";

/** Linux only — macOS/Windows use native titlebar controls, not the tab rail. */
export function tabBarTrailingControls(): JSX.Element | undefined {
  if (tauriPlatform() !== "linux") return undefined;
  return <WindowControls />;
}

async function windowApi() {
  const { getCurrentWindow } = await import("@tauri-apps/api/window");
  return getCurrentWindow();
}

/** Linux undecorated window controls — macOS uses overlay traffic lights. */
export function WindowControls() {
  if (tauriPlatform() !== "linux") return null;

  return (
    <div class="den-shell-window-controls" data-testid="window-controls">
      <button
        type="button"
        class="den-shell-window-control"
        aria-label="Minimize window"
        data-testid="window-minimize"
        onClick={() => void windowApi().then((win) => win.minimize())}
      >
        ─
      </button>
      <button
        type="button"
        class="den-shell-window-control"
        aria-label="Maximize window"
        data-testid="window-maximize"
        onClick={() => void windowApi().then((win) => win.toggleMaximize())}
      >
        □
      </button>
      <button
        type="button"
        class="den-shell-window-control-close den-shell-window-control"
        aria-label="Close window"
        data-testid="window-close"
        onClick={() => void windowApi().then((win) => win.close())}
      >
        ×
      </button>
    </div>
  );
}
