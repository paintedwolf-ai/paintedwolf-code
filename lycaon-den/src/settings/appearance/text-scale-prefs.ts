/** Device-level product type scale, composed with macOS accessibility size. */
import { createSignal } from "solid-js";
import {
  PRODUCT_TEXT_SCALES,
  type DenAppearancePrefs,
  type ProductTextScale,
} from "../../../shared/app-state-types.ts";
import {
  LINUX_TEXT_SCALING_CATEGORY,
  applyProductTextScale,
} from "../../platform/desktop/accessibility-text-size.ts";
import type { TauriPlatform } from "../../platform/runtime.ts";
import { getAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { patchAppearancePrefs } from "./appearance-state.ts";

export const DEFAULT_PRODUCT_TEXT_SCALE: ProductTextScale = 1;

/** Settings copy stating what the host adds to this reading-size choice. */
export function textSizeHint(
  platform: TauriPlatform | null,
  hostCategory: string,
): string {
  if (platform === "macos") {
    return "Adjusts the app's reading size in addition to your Mac's accessibility text size.";
  }
  if (platform === "linux") {
    return hostCategory === LINUX_TEXT_SCALING_CATEGORY
      ? "Adjusts the app's reading size in addition to your desktop's text scaling, which the window already applies."
      : "Adjusts the app's reading size. This desktop reports no text scaling preference, so this is the only text size control.";
  }
  return "Adjusts the app's reading size.";
}

const [selection, setSelection] = createSignal<ProductTextScale>(
  DEFAULT_PRODUCT_TEXT_SCALE,
);

export function productTextScale(): ProductTextScale {
  return selection();
}

export function resolveProductTextScale(
  prefs?: DenAppearancePrefs,
): ProductTextScale {
  return prefs?.textScale ?? DEFAULT_PRODUCT_TEXT_SCALE;
}

function applySelection(next: ProductTextScale): void {
  setSelection(next);
  applyProductTextScale(next);
}

export function syncProductTextScaleFromSnapshot(): void {
  applySelection(resolveProductTextScale(getAppStateSnapshot().appearance));
}

export async function setProductTextScale(next: ProductTextScale): Promise<void> {
  if (!PRODUCT_TEXT_SCALES.includes(next)) return;
  applySelection(next);
  await patchAppearancePrefs({ textScale: next });
}

/** Test-only reset. */
export function resetProductTextScaleForTests(
  prefs?: DenAppearancePrefs,
): void {
  applySelection(resolveProductTextScale(prefs));
}
