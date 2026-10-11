import { batch, createSignal } from "solid-js";
import type { DenLayoutPrefs } from "../../shared/app-state-types.ts";
import { windowSubject } from "../platform/windows/window-subject.ts";
import { getAppStateSnapshot } from "../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../store/app-state-background-write.ts";
import { beginShellLayoutBusy, endShellLayoutBusy } from "../shell/shell-layout-busy.ts";
import { DEFAULT_PLACEMENT, DEFAULT_WORKSPACE_ORIENTATION } from "../shell/stage-placement.ts";

type LayoutScope = "persistent-main" | "ephemeral-peer";
const scope: LayoutScope =
  windowSubject() == null ? "persistent-main" : "ephemeral-peer";
const [preferences, setPreferences] = createSignal<DenLayoutPrefs>({});
let peerInitialized = false;

export function currentPreferences(): DenLayoutPrefs {
  return preferences();
}

export async function commitPreferences(
  next: DenLayoutPrefs,
  options?: {
    settleLayout?: boolean;
    applyRelatedState?: () => void;
  },
): Promise<void> {
  if (options?.settleLayout) beginShellLayoutBusy();
  try {
    batch(() => {
      setPreferences(next);
      options?.applyRelatedState?.();
    });
  } finally {
    if (options?.settleLayout) endShellLayoutBusy();
  }
  if (scope === "persistent-main") {
    await persistAppStateInBackground({ layout: next });
  }
}

/** Peer windows seed once and keep local state. */
export function syncLayoutFromSnapshot(): void {
  const stored = getAppStateSnapshot().layout ?? {};
  if (scope === "ephemeral-peer") {
    if (peerInitialized) return;
    setPreferences({ ...stored, mode: "inline", navCollapsed: true });
    peerInitialized = true;
    return;
  }
  setPreferences(stored);
}

export function resetLayoutPreferencesForTests(): void {
  peerInitialized = false;
  setPreferences({ mode: DEFAULT_PLACEMENT, workspaceOrientation: DEFAULT_WORKSPACE_ORIENTATION });
}
