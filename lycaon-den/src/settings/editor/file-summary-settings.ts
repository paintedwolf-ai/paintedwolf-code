import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { clearFileBriefingLive } from "../../files/components/file-briefing-live.ts";

const [known, setKnown] = createSignal(false);
const [enabled, setEnabled] = createSignal(true);
type SettingState = {
  client: LycaonClient | null;
  hostEnabled: boolean;
  requested: boolean | null;
  draining: Promise<void> | null;
  revision: number;
};
let currentState: SettingState | undefined;

function stateFor(client: LycaonClient): SettingState {
  if (currentState?.client === null) currentState.client = client;
  if (currentState?.client !== client) {
    currentState = { client, hostEnabled: true, requested: null, draining: null, revision: 0 };
    setKnown(false);
  }
  return currentState;
}

function show(next: boolean): void {
  setEnabled(next);
  if (!next) clearFileBriefingLive();
}

export function fileSummariesSettingKnown(): boolean {
  return known();
}

export function fileSummariesEnabled(): boolean {
  return enabled();
}

export async function refreshFileSummariesSetting(
  client: LycaonClient,
): Promise<boolean> {
  const state = stateFor(client);
  const revision = ++state.revision;
  const response = await client.getFileSummariesSettings();
  if (currentState === state && state.revision === revision) {
    state.hostEnabled = response.enabled;
    setKnown(true);
    if (!state.draining) show(response.enabled);
  }
  return response.enabled;
}

/** Pending saves coalesce to the latest value; failure restores the host value. */
export function saveFileSummariesEnabled(
  client: LycaonClient,
  next: boolean,
): Promise<void> {
  const state = stateFor(client);
  state.revision++;
  show(next);
  state.requested = next;
  state.draining ??= drain(client, state);
  return state.draining;
}

async function drain(client: LycaonClient, state: SettingState): Promise<void> {
  try {
    while (currentState === state && state.requested !== null) {
      const target = state.requested;
      state.requested = null;
      try {
        const response = await client.updateFileSummariesSettings({ enabled: target });
        if (currentState !== state) return;
        state.revision++;
        state.hostEnabled = response.enabled;
        setKnown(true);
      } catch (error) {
        if (currentState === state) {
          if (state.requested !== null) continue;
          show(state.hostEnabled);
        }
        throw error;
      }
    }
    if (currentState === state) show(state.hostEnabled);
  } finally {
    state.draining = null;
  }
}

export function resetFileSummariesSetting(): void {
  currentState = undefined;
  setKnown(false);
  setEnabled(true);
}
