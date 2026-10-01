/** Applies the host accessibility text scale to `--den-text-scale`. */

import { createSignal } from "solid-js";
import { isTauriRuntime } from "../runtime.ts";
import { listenHostEvent } from "../windows/window-channel.ts";
import { refreshLayoutBands } from "../../layout/layout-bands.ts";

export const TEXT_SIZE_CHANGED_EVENT = "accessibility://text-size-changed";

export type TextSizePayload = {
  category: string;
  textScale: number;
  /** Monotonic versions reject snapshots older than the latest live update. */
  revision: number;
};

/** Largest scale the host reports: the AX5 body size over the 17pt default. */
export const MAX_TEXT_SCALE = 53 / 17;

let operatingSystemTextScale = 1;
let productTextScale = 1;
const [effectiveTextScale, setEffectiveTextScale] = createSignal(1);
export { effectiveTextScale };

/** Apply the composed operating-system and product reading scale. */
function applyEffectiveTextScale(): void {
  if (typeof document === "undefined") return;
  document.documentElement.style.setProperty(
    "--den-text-scale",
    String(operatingSystemTextScale * productTextScale),
  );
  setEffectiveTextScale(operatingSystemTextScale * productTextScale);
  // Layout bands resolve rem thresholds against the updated root font size.
  refreshLayoutBands();
}

/** Set the operating-system accessibility scale on `:root` / `documentElement`. */
export function applyTextScale(textScale: number): void {
  operatingSystemTextScale = textScale;
  applyEffectiveTextScale();
}

/** Set the device's product reading-scale multiplier. */
export function applyProductTextScale(textScale: number): void {
  productTextScale = textScale;
  applyEffectiveTextScale();
}

/** Test-only reset for the two composed scale inputs. */
export function resetTextScaleForTests(): void {
  operatingSystemTextScale = 1;
  productTextScale = 1;
  applyEffectiveTextScale();
}

export function applyTextSizePayload(payload: TextSizePayload): void {
  if (payload.revision < latestTextSizeRevision) return;
  latestTextSizeRevision = payload.revision;
  applyTextScale(payload.textScale);
}

let latestTextSizeRevision = -1;

/** Apply the current host scale and subscribe to updates. */
export async function setupAccessibilityTextSize(): Promise<void> {
  if (!isTauriRuntime()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await listenHostEvent<TextSizePayload>(TEXT_SIZE_CHANGED_EVENT, (event) => {
      applyTextSizePayload(event.payload);
    });
    const initial = await invoke<TextSizePayload>("accessibility_preferred_text_size");
    applyTextSizePayload(initial);
  } catch {
    /* Keep the CSS default when host lookup fails. */
  }
}
