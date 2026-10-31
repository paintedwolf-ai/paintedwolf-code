/** Routes in-app links (such as loopback holder attribution and folder-removal errors) to the Running context stage. */

import { listenHostEvent } from "../windows/window-channel.ts";

export type OpenRunningRequest = {
  projectId?: string;
};

type OpenRunningSink = (req?: OpenRunningRequest) => void;

let sink: OpenRunningSink | null = null;
let pending: OpenRunningRequest | undefined;
let hasPending = false;

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
}
