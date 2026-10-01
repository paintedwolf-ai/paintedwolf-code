import type { Project } from "../api/types.ts";
import type { LycaonClient } from "../api/client.ts";
import type { SettingsStore } from "../store/settings-store.ts";
import {
  refreshSettingsSlices,
  type SettingsInvalidationSlice,
} from "./settings-actions.ts";

/** Matches EventHub server debounce for settings-related topics. */
export const SETTINGS_INVALIDATION_MS = 80;

export type SettingsInvalidationScheduler = {
  schedule: (slices: readonly SettingsInvalidationSlice[]) => void;
  cancel: () => void;
};

export function createSettingsInvalidationScheduler(
  store: SettingsStore,
  client: () => LycaonClient | null,
  projects: () => readonly Project[],
  projectDir: () => string | undefined,
): SettingsInvalidationScheduler {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pending = new Set<SettingsInvalidationSlice>();

  const flush = () => {
    timer = undefined;
    const slices = [...pending];
    pending = new Set();
    const c = client();
    if (!c || slices.length === 0) return;
    void refreshSettingsSlices(store, c, projects(), slices, projectDir()).catch(
      (err) =>
        store.actions.setError(err instanceof Error ? err.message : String(err)),
    );
  };

  return {
    schedule(slices) {
      for (const slice of slices) pending.add(slice);
      if (timer) clearTimeout(timer);
      timer = setTimeout(flush, SETTINGS_INVALIDATION_MS);
    },
    cancel() {
      if (timer) clearTimeout(timer);
      timer = undefined;
      pending = new Set();
    },
  };
}
