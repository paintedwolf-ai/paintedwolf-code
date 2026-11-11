import type { LycaonClient } from "../../api/client.ts";
import { refreshFindings } from "../actions/findings-actions.ts";
import { refreshQueue } from "../actions/queue-actions.ts";
import { refreshProgress } from "../progress/progress-actions.ts";
import { refreshCoordinatorContext } from "../workflow/coordinator-context-actions.ts";
import {
  applyBackgroundProcessOutputs,
  refreshBackgroundProcessSnapshots,
} from "../tool/background-process-store.ts";
import {
  applyPreviewAttachment,
  refreshLivePreviewSnapshots,
} from "../visual/preview-store.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { Session, SessionBootstrap } from "../../api/types.ts";
import { noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { sessionScope } from "../../notices/notice-scope.ts";

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

function hydrationForEpoch(
  appStore: AppStore,
  sessionId: string,
  epoch: number,
): SessionChromeHydration {
  let sessions = hydrationByStore.get(appStore);
  if (!sessions) {
    sessions = new Map();
    hydrationByStore.set(appStore, sessions);
  }
  let hydration = sessions.get(sessionId);
  if (!hydration || hydration.epoch !== epoch) {
    hydration = { epoch, hydrated: false, inflight: null };
  }
  sessions.delete(sessionId);
  sessions.set(sessionId, hydration);
  trimHydrations(sessions);
  return hydration;
}

function hydrationFor(appStore: AppStore, sessionId: string): SessionChromeHydration {
  return hydrationForEpoch(appStore, sessionId, appStore.state.sessionViewEpoch);
}

function reportAuxiliaryChromeError(session: Session, err: unknown): void {
  noticeReporterFor(sessionScope(session.project_id, session.id)).reportError(err);
}

/** Install store-backed bootstrap slices and return the resulting view epoch. */
export function installSessionBootstrapChrome(
  appStore: AppStore,
  bootstrap: SessionBootstrap,
  session = bootstrap.session,
): number {
  hydrationForEpoch(appStore, session.id, appStore.state.sessionViewEpoch + 1).hydrated = true;
  appStore.actions.installSessionBootstrap(bootstrap, session);
  hydrationFor(appStore, session.id).hydrated = true;
  return appStore.state.sessionViewEpoch;
}

/** Apply auxiliary process and preview mirrors without blocking transcript hydration. */
export function applySessionBootstrapAuxiliaryChrome(
  appStore: AppStore,
  bootstrap: SessionBootstrap,
  session = bootstrap.session,
): void {
  let ok = true;
  try {
    applyBackgroundProcessOutputs(
      bootstrap.session.id,
      bootstrap.background_outputs,
    );
  } catch (err) {
    ok = false;
    reportAuxiliaryChromeError(session, err);
  }
  try {
    for (const preview of bootstrap.previews ?? []) {
      applyPreviewAttachment(preview);
    }
  } catch (err) {
    ok = false;
    reportAuxiliaryChromeError(session, err);
  }
  hydrationFor(appStore, session.id).hydrated = ok;
}

/** Apply bootstrap-supplied session views. */
export function applySessionBootstrapChrome(
  appStore: AppStore,
  bootstrap: SessionBootstrap,
  session = bootstrap.session,
): number {
  const epoch = installSessionBootstrapChrome(appStore, bootstrap, session);
  applySessionBootstrapAuxiliaryChrome(appStore, bootstrap, session);
  return epoch;
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
