/** Routes security finding focus requests into the security stage. */

type OpenFindingRequest = {
  projectId: string;
  rootId?: string;
  fingerprint: string;
  scanId?: string;
};

type OpenFindingSink = (req: OpenFindingRequest) => void;

let sink: OpenFindingSink | null = null;
let pending: OpenFindingRequest | null = null;

/** Pending focus request for the mounted pane. */
let focusRequest: OpenFindingRequest | null = null;
const focusListeners = new Set<() => void>();

export function registerOpenFindingSink(next: OpenFindingSink): () => void {
  sink = next;
  if (pending) {
    next(pending);
    pending = null;
  }
  return () => {
    if (sink === next) sink = null;
  };
}

export function openFinding(req: OpenFindingRequest): void {
  if (sink) sink(req);
  else pending = req;
}

export function setFindingFocusRequest(req: OpenFindingRequest | null): void {
  focusRequest = req;
  for (const l of focusListeners) l();
}

export function takeFindingFocusRequest(): OpenFindingRequest | null {
  const req = focusRequest;
  focusRequest = null;
  return req;
}

export function onFindingFocusRequest(listener: () => void): () => void {
  focusListeners.add(listener);
  return () => {
    focusListeners.delete(listener);
  };
}

export function resetOpenFindingForTests(): void {
  sink = null;
  pending = null;
  focusRequest = null;
  focusListeners.clear();
}
