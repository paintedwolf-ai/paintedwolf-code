/** Routes in-app links (such as loopback holder attribution and folder-removal errors) to the Running context stage. */

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
  return () => {
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
