import type { LycaonClient } from "../../api/client.ts";
import { refreshFindings } from "../actions/findings-actions.ts";
import { refreshQueue } from "../actions/queue-actions.ts";
import { refreshProgress } from "../progress/progress-actions.ts";
import { refreshCoordinatorContext } from "../workflow/coordinator-context-actions.ts";
import { refreshBackgroundProcessSnapshots } from "../tool/background-process-store.ts";
import { refreshLivePreviewSnapshots } from "../visual/preview-store.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { SessionBootstrap } from "../../api/types.ts";
import { applyBackgroundProcessOutputs } from "../tool/background-process-store.ts";
import { applyPreviewAttachment } from "../visual/preview-store.ts";

type SessionChromeHydration = {
  epoch: number;
  hydrated: boolean;
  inflight: Promise<void> | null;
};

let hydrationByStore = new WeakMap<AppStore, Map<string, SessionChromeHydration>>();
export const SESSION_CHROME_HYDRATION_CAP = 128;

function trimHydrations(sessions: Map<string, SessionChromeHydration>): void {
  while (sessions.size > SESSION_CHROME_HYDRATION_CAP) {
    const oldest = sessions.keys().next().value;
    if (oldest === undefined) return;
    sessions.delete(oldest);
  }
}

function hydrationFor(appStore: AppStore, sessionId: string): SessionChromeHydration {
  let sessions = hydrationByStore.get(appStore);
  if (!sessions) {
    sessions = new Map();
    hydrationByStore.set(appStore, sessions);
  }
  const epoch = appStore.state.sessionViewEpoch;
  let hydration = sessions.get(sessionId);
  if (!hydration || hydration.epoch !== epoch) {
    hydration = { epoch, hydrated: false, inflight: null };
  }
  sessions.delete(sessionId);
  sessions.set(sessionId, hydration);
  trimHydrations(sessions);
  return hydration;
}

/** Apply bootstrap-supplied session views. */
export function applySessionBootstrapChrome(
  appStore: AppStore,
  bootstrap: SessionBootstrap,
  session = bootstrap.session,
): void {
  appStore.actions.installSessionBootstrap(bootstrap, session);
  applyBackgroundProcessOutputs(
    bootstrap.session.id,
    bootstrap.background_outputs,
  );
  for (const preview of bootstrap.previews) applyPreviewAttachment(preview);
  const hydration = hydrationFor(appStore, session.id);
  hydration.hydrated = true;
}

/** Hydrates session views once per epoch. */
export function ensureSessionChrome(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
): Promise<void> {
  const sid = sessionId.trim();
  if (!sid) return Promise.resolve();
  const hydration = hydrationFor(appStore, sid);
  if (hydration.hydrated) return Promise.resolve();
  if (hydration.inflight) return hydration.inflight;
  const settled = (refresh: Promise<unknown>) => refresh.then(() => true, () => false);
  hydration.inflight = Promise.all([
    settled(refreshProgress(appStore, client, sid)),
    settled(refreshFindings(appStore, client, sid)),
    settled(refreshQueue(appStore, client, sid)),
    settled(refreshCoordinatorContext(appStore, client, sid)),
    settled(refreshBackgroundProcessSnapshots(client, sid)),
    settled(refreshLivePreviewSnapshots(client, sid)),
  ]).then((results) => {
    hydration.hydrated = results.every(Boolean);
    hydration.inflight = null;
  });
  return hydration.inflight;
}

export function resetSessionChromeHydrationForTests(): void {
  hydrationByStore = new WeakMap();
}
