type BackendReachabilityObserver = {
  reachable: () => void;
  unreachable: () => void;
};

let observer: BackendReachabilityObserver | null = null;
let revision = 0;

export function setBackendReachabilityObserver(
  next: BackendReachabilityObserver | null,
): void {
  revision++;
  observer = next;
}

export function noteBackendReachable(): void {
  revision++;
  observer?.reachable();
}

export function noteBackendUnreachable(): void {
  revision++;
  observer?.unreachable();
}

type BackendReachability = "reachable" | "unreachable";

/** BackendTransportError records the independent reachability probe. */
export class BackendTransportError extends Error {
  readonly cause: unknown;
  readonly reachability: BackendReachability;

  constructor(cause: unknown, reachability: BackendReachability) {
    super("The backend request could not be completed.");
    this.name = "BackendTransportError";
    this.cause = cause;
    this.reachability = reachability;
  }
}

export function isBackendUnreachableError(
  err: unknown,
): err is BackendTransportError {
  return (
    err instanceof BackendTransportError && err.reachability === "unreachable"
  );
}

export async function confirmBackendReachability(
  probe: (signal: AbortSignal) => Promise<boolean>,
): Promise<BackendReachability> {
  const startedAt = ++revision;
  const controller = new AbortController();
  const timer = globalThis.setTimeout(() => controller.abort(), 1500);
  try {
    const reachable = await probe(controller.signal);
    if (revision === startedAt) {
      if (reachable) noteBackendReachable();
      else noteBackendUnreachable();
    }
    return reachable ? "reachable" : "unreachable";
  } catch {
    if (revision === startedAt) noteBackendUnreachable();
    return "unreachable";
  } finally {
    globalThis.clearTimeout(timer);
  }
}
