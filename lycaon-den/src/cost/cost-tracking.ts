import type { SettingsStore } from "../store/settings-store.ts";

/** True when Settings → Cost main tracking is on. */
export function costTrackingEnabled(store: SettingsStore): boolean {
  return store.state.pricing?.cost_tracking_enabled === true;
}

/** ISO stamp from off→on, or null when tracking is off / unset. */
export function costTrackingSince(store: SettingsStore): string | null {
  return store.state.pricing?.cost_tracking_since_at ?? null;
}
