/** Routes in-app links and watch actions to the Activity context stage. */

import { createSignal } from "solid-js";
import { listenHostEvent } from "../windows/window-channel.ts";

type ActivityWatchPrefillIntent = {
  sessionId: string;
  handle: string;
  reviewChanges?: boolean;
};

export type OpenRunningRequest = {
  projectId?: string;
  watchId?: string;
  sessionId?: string;
  prefillFromWatch?: ActivityWatchPrefillIntent;
  createNew?: boolean;
};

type ActivityFocusRequest = OpenRunningRequest & {
  serial: number;
};

type OpenRunningSink = (req?: OpenRunningRequest) => void;

let sink: OpenRunningSink | null = null;
let pending: OpenRunningRequest | undefined;
let hasPending = false;
let nextSerial = 0;

const [activityFocusRequest, setActivityFocusRequest] =
  createSignal<ActivityFocusRequest | null>(null);

export { activityFocusRequest };

export function clearActivityFocusRequest(serial?: number): void {
  const cur = activityFocusRequest();
  if (!cur) return;
  if (serial !== undefined && cur.serial !== serial) return;
  setActivityFocusRequest(null);
}

export function registerOpenRunningStageSink(next: OpenRunningSink): () => void {
  sink = next;
  if (hasPending) {
    const req = pending;
    pending = undefined;
    hasPending = false;
    next(req);
  }
  let disposed = false;
  let stopHostListener: (() => void) | undefined;
  void listenHostEvent<string>("app-exit-open-running", ({ payload }) => {
    const projectId =
      typeof payload === "string" && payload.trim() ? payload.trim() : undefined;
    openRunningStage(projectId ? { projectId } : undefined);
  }).then((unlisten) => {
    if (disposed) unlisten();
    else stopHostListener = unlisten;
  });
  return () => {
    disposed = true;
    stopHostListener?.();
    if (sink === next) sink = null;
  };
}

export function openRunningStage(req?: OpenRunningRequest): void {
  if (
    req &&
    (req.watchId || req.sessionId || req.prefillFromWatch || req.createNew)
  ) {
    nextSerial += 1;
    setActivityFocusRequest({ ...req, serial: nextSerial });
  }
  if (sink) {
    sink(req);
  } else {
    pending = req;
    hasPending = true;
  }
}

export function resetOpenRunningForTests(): void {
  sink = null;
  pending = undefined;
  hasPending = false;
  nextSerial = 0;
  setActivityFocusRequest(null);
}
