/** Reads display preferences from the native host or browser media queries. */
import { createSignal } from "solid-js";
import {
  NO_SYSTEM_ADJUSTMENTS,
  type SystemThemeAdjustments,
} from "../../contributions/theme-adjustments.ts";
import { isTauriRuntime } from "../runtime.ts";
import { listenHostEvent } from "../windows/window-channel.ts";

export const SYSTEM_APPEARANCE_CHANGED_EVENT = "appearance://system-changed";

export type SystemAppearancePayload = {
  increaseContrast: boolean;
  reduceTransparency: boolean;
  accent: string | null;
  /** Monotonic version rejects stale snapshots. */
  revision: number;
};

type SystemAppearance = Omit<SystemAppearancePayload, "revision">;

const NEUTRAL: SystemAppearance = {
  increaseContrast: false,
  reduceTransparency: false,
  accent: null,
};

const [appearance, setAppearance] = createSignal<SystemAppearance>(NEUTRAL);
const listeners = new Set<() => void>();
let latestRevision = -1;

/** The token adjustments the current system preferences call for. */
export function systemThemeAdjustments(): SystemThemeAdjustments {
  const current = appearance();
  if (!current.increaseContrast && !current.accent) return NO_SYSTEM_ADJUSTMENTS;
  return { accent: current.accent, increaseContrast: current.increaseContrast };
}

export function onSystemAppearanceChange(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function stampRoot(next: SystemAppearance): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  if (next.increaseContrast) root.setAttribute("data-den-contrast", "more");
  else root.removeAttribute("data-den-contrast");
  if (next.reduceTransparency) root.setAttribute("data-den-transparency", "reduced");
  else root.removeAttribute("data-den-transparency");
}

function commit(next: SystemAppearance): void {
  const prev = appearance();
  stampRoot(next);
  if (
    prev.increaseContrast === next.increaseContrast &&
    prev.reduceTransparency === next.reduceTransparency &&
    prev.accent === next.accent
  ) {
    return;
  }
  setAppearance(next);
  for (const listener of listeners) listener();
}

export function applySystemAppearancePayload(payload: SystemAppearancePayload): void {
  if (payload.revision < latestRevision) return;
  latestRevision = payload.revision;
  commit({
    increaseContrast: payload.increaseContrast,
    reduceTransparency: payload.reduceTransparency,
    accent: payload.accent,
  });
}

function setupMediaQueryFallback(): void {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
  const contrast = window.matchMedia("(prefers-contrast: more)");
  const transparency = window.matchMedia("(prefers-reduced-transparency: reduce)");
  const read = () =>
    commit({
      increaseContrast: contrast.matches,
      reduceTransparency: transparency.matches,
      accent: null,
    });
  contrast.addEventListener("change", read);
  transparency.addEventListener("change", read);
  read();
}

/** Applies the current preferences and follows changes. */
export async function setupSystemAppearance(): Promise<void> {
  if (!isTauriRuntime()) {
    setupMediaQueryFallback();
    return;
  }
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await listenHostEvent<SystemAppearancePayload>(SYSTEM_APPEARANCE_CHANGED_EVENT, (event) => {
      applySystemAppearancePayload(event.payload);
    });
    applySystemAppearancePayload(await invoke<SystemAppearancePayload>("system_appearance"));
  } catch {
    /* Without the host reading, the theme paints as authored. */
  }
}

/** Test-only reset. */
export function resetSystemAppearanceForTests(): void {
  latestRevision = -1;
  listeners.clear();
  commit(NEUTRAL);
  setAppearance(NEUTRAL);
}
