import { createSignal } from "solid-js";
import { invoke } from "@tauri-apps/api/core";
import { reconcileOsColorScheme, osColorSchemeIsDark } from "../../theme.ts";
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";

function tauriThemeIsDark(theme: string | null | undefined): boolean {
  if (theme === "dark") return true;
  if (theme === "light") return false;
  return osColorSchemeIsDark();
}

async function waitForWindowGeometryPaint(): Promise<void> {
  if (typeof requestAnimationFrame !== "function") return;
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}

/** Track window focus; browser runtimes remain focused. */
export const [windowFocused, setWindowFocused] = createSignal(true);

const [geometryClaimDepth, setGeometryClaimDepth] = createSignal(0);

/** True while a native resize is pending, when the viewport still reports the
 *  width the window is leaving. */
export function windowGeometryClaimPending(): boolean {
  return geometryClaimDepth() > 0;
}

/** Apply runtime platform classes before first render. */
export function tagTauriPlatformClasses(): void {
  if (!isTauriRuntime()) return;
  const root = document.documentElement;
  root.classList.add("den-tauri");
  const platform = tauriPlatform();
  if (platform) root.classList.add(`den-tauri-${platform}`);
  if (platform === "macos" || platform === "linux") {
    root.classList.add("den-custom-chrome");
  }
}

/** Show the window once the document has something to paint. */
export async function revealWindow(): Promise<void> {
  if (!isTauriRuntime()) return;
  try {
    await invoke("den_reveal_window");
  } catch (err) {
    console.debug("[window-chrome] reveal failed", err);
  }
}

/** Focus the app window without surfacing shell errors. */
export async function focusAppWindow(): Promise<void> {
  if (!isTauriRuntime()) return;
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    await getCurrentWindow().setFocus();
  } catch (err) {
    console.debug("[window-chrome] setFocus failed", err);
  }
}

/** `no-room`: the window is fullscreen or maximized, or the work area is full. */
export type WidenOutcome = "claimed" | "no-room";

/** Claims run one at a time; a claim never stacks on a width still landing. */
let claimChain: Promise<unknown> = Promise.resolve();
/** Logical pixels every finished claim has added to the window. */
let grownPx = 0;

/**
 * Grow the window within the monitor work area. A delta is measured against
 * the viewport the caller saw, so growth that lands first counts toward it.
 */
export function widenWindowBy(deltaPx: number): Promise<WidenOutcome> {
  if (!Number.isFinite(deltaPx) || deltaPx <= 0) return Promise.resolve("claimed");
  if (!isTauriRuntime()) return Promise.resolve("no-room");
  setGeometryClaimDepth((depth) => depth + 1);
  const grownWhenAsked = grownPx;
  const claim = claimChain.then(async (): Promise<WidenOutcome> => {
    try {
      const remaining = deltaPx - (grownPx - grownWhenAsked);
      if (remaining <= 0) return "claimed";
      return await growWindowBy(remaining);
    } catch (err) {
      console.debug("[window-chrome] widen failed", err);
      return "no-room";
    } finally {
      setGeometryClaimDepth((depth) => Math.max(0, depth - 1));
    }
  });
  claimChain = claim;
  return claim;
}

/** One native write moves and sizes the window together. */
async function growWindowBy(deltaPx: number): Promise<WidenOutcome> {
  const { getCurrentWindow, currentMonitor } = await import("@tauri-apps/api/window");
  const win = getCurrentWindow();
  // Each read is an IPC round trip, so batch them.
  const [fullscreen, maximized, scale] = await Promise.all([
    win.isFullscreen(),
    win.isMaximized(),
    win.scaleFactor(),
  ]);
  // Fullscreen and maximized windows have no horizontal room.
  if (fullscreen || maximized) return "no-room";
  const [innerSize, outerSize, originPoint, monitor] = await Promise.all([
    win.innerSize(),
    win.outerSize(),
    win.outerPosition(),
    currentMonitor(),
  ]);
  const inner = innerSize.toLogical(scale);
  const outer = outerSize.toLogical(scale);
  const origin = originPoint.toLogical(scale);
  const area = monitor
    ? {
        x: monitor.workArea.position.toLogical(monitor.scaleFactor).x,
        width: monitor.workArea.size.toLogical(monitor.scaleFactor).width,
      }
    : null;
  // The bounds take the inner size; the frame around it still has to fit.
  const frame = Math.max(0, outer.width - inner.width);
  const room = area ? area.width - frame : Number.POSITIVE_INFINITY;
  const width = Math.round(Math.min(inner.width + deltaPx, room));
  if (width <= inner.width) return "no-room";
  // Rightward growth stays within the display.
  const overflow = area ? origin.x + width + frame - (area.x + area.width) : 0;
  const x = overflow > 0 && area ? Math.max(area.x, origin.x - overflow) : origin.x;
  await invoke("den_set_window_bounds", {
    x: Math.round(x),
    y: Math.round(origin.y),
    width,
    height: Math.round(inner.height),
  });
  grownPx += width - inner.width;
  // Native resize acknowledgement can precede the webview's new CSS box.
  await waitForWindowGeometryPaint();
  return "claimed";
}

/** Follow system appearance and window focus changes. */
export async function setupWindowChrome(): Promise<void> {
  if (!isTauriRuntime()) return;
  tagTauriPlatformClasses();
  const { getCurrentWindow } = await import("@tauri-apps/api/window");
  const win = getCurrentWindow();

  // A null theme follows system appearance.
  await win.setTheme(null);

  const initial = await win.theme();
  reconcileOsColorScheme(tauriThemeIsDark(initial));

  await win.onThemeChanged(({ payload }) => {
    reconcileOsColorScheme(tauriThemeIsDark(payload));
  });

  try {
    setWindowFocused(await win.isFocused());
  } catch {
    setWindowFocused(true);
  }
  await win.onFocusChanged(({ payload }) => {
    setWindowFocused(payload);
  });
}
