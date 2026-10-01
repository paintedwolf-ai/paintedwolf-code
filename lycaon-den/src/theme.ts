import { isTauriRuntime } from "./platform/runtime.ts";

export type OsColorSchemeListener = (dark: boolean) => void;

const DARK_MEDIA = "(prefers-color-scheme: dark)";

/** Last observed system scheme. */
let resolvedDark: boolean | null = null;

export function osColorSchemeIsDark(): boolean {
  if (resolvedDark != null) return resolvedDark;
  if (typeof window.matchMedia !== "function") return false;
  return window.matchMedia(DARK_MEDIA).matches;
}

function recordOsColorScheme(dark: boolean): void {
  resolvedDark = dark;
}

function subscribePrefersColorScheme(listener: OsColorSchemeListener): () => void {
  if (typeof window.matchMedia !== "function") {
    listener(false);
    return () => undefined;
  }
  const mq = window.matchMedia(DARK_MEDIA);
  const onChange = (event: MediaQueryListEvent) => {
    listener(event.matches);
  };
  listener(mq.matches);
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}

const listeners = new Set<OsColorSchemeListener>();
let watchStarted = false;
let stopWatch: (() => void) | undefined;

export function reconcileOsColorScheme(dark: boolean): void {
  recordOsColorScheme(dark);
  for (const listener of listeners) {
    listener(dark);
  }
}

function ensureOsColorSchemeWatch(): void {
  if (watchStarted) return;
  watchStarted = true;
  stopWatch = subscribePrefersColorScheme(reconcileOsColorScheme);
}

/** Subscribes and immediately reports the current system scheme. */
export function onOsColorSchemeChange(listener: OsColorSchemeListener): () => void {
  // Native window events drive desktop updates.
  if (!isTauriRuntime()) {
    ensureOsColorSchemeWatch();
  }
  listeners.add(listener);
  listener(osColorSchemeIsDark());
  return () => {
    listeners.delete(listener);
  };
}

export function syncOsColorScheme(): void {
  recordOsColorScheme(osColorSchemeIsDark());
  if (!isTauriRuntime()) {
    ensureOsColorSchemeWatch();
  }
}

/** Test-only system scheme reset. */
export function resetOsColorSchemeWatchForTests(): void {
  stopWatch?.();
  stopWatch = undefined;
  watchStarted = false;
  resolvedDark = null;
  listeners.clear();
}
