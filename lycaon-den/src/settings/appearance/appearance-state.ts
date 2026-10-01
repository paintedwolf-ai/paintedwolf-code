import type { DenAppearancePrefs } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

/** Appearance is one wholesale app-state slice shared by theme and type. */
export function mergeAppearancePrefs(
  current: DenAppearancePrefs | undefined,
  patch: DenAppearancePrefs,
): DenAppearancePrefs {
  return { ...current, ...patch };
}

export function patchAppearancePrefs(patch: DenAppearancePrefs): Promise<void> {
  return persistAppStateInBackground({
    appearance: mergeAppearancePrefs(getAppStateSnapshot().appearance, patch),
  });
}
