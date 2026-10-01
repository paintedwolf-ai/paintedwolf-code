import { LycaonApiError, isApiErrorCode } from "../../api/http.ts";
import type { SourceViewsClient } from "../../api/source-views-client.ts";
import type { SourceViewportInterest } from "../../api/types.ts";

type InterestLease = {
  id: string;
  view: string;
  sequence: number;
  key: string;
  closed: boolean;
  pending?: SourceViewportInterest;
  timer?: ReturnType<typeof setTimeout>;
  inFlight?: Promise<void>;
  failures: number;
};

function retryDelay(error: unknown, failures: number): number | undefined {
  const backoff = Math.min(30_000, 250 * 2 ** Math.min(failures - 1, 7));
  if (error instanceof LycaonApiError) {
    if (!isApiErrorCode(error, ["rate_limited", "source_view_capacity"])) return undefined;
    return Math.max(backoff, error.retryAfterMs ?? 0);
  }
  return backoff;
}

/** One request in flight per lease; later motion replaces its pending demand. */
export class ViewportInterest {
  private lease?: InterestLease;

  constructor(private readonly client: SourceViewsClient, private readonly project: string) {}

  update(view: string, presentation: string, start: number, end: number, direction: number): void {
    if (this.lease?.view !== view) this.clear();
    const lease = this.lease ??= {
      id: crypto.randomUUID(), view, sequence: 0, key: "", closed: false, failures: 0,
    };
    start = Math.max(0, Math.floor(start / 200) * 200);
    end = Math.min(start + 800, Math.max(start + 200, Math.ceil(end / 200) * 200));
    const key = `${presentation}:${start}:${end}:${direction}`;
    if (key === lease.key) return;
    lease.key = key;
    lease.pending = { presentation_id: presentation, sequence: ++lease.sequence, start, end, direction };
    this.schedule(lease, 100);
  }

  private schedule(lease: InterestLease, delay: number): void {
    if (lease.closed || lease.timer !== undefined || lease.inFlight || !lease.pending) return;
    lease.timer = setTimeout(() => { lease.timer = undefined; this.send(lease); }, delay);
  }

  private send(lease: InterestLease): void {
    const pending = lease.pending;
    if (!pending || lease.closed) return;
    lease.pending = undefined;
    let delay = 100;
    lease.inFlight = this.client.replaceSourceViewportInterest(
      this.project, lease.view, lease.id, pending, AbortSignal.timeout(10_000),
    ).then(() => { lease.failures = 0; }).catch((error: unknown) => {
      const retry = retryDelay(error, ++lease.failures);
      if (retry !== undefined) {
        lease.pending ??= pending;
        delay = retry;
      } else {
        lease.key = "";
      }
    }).finally(() => {
      lease.inFlight = undefined;
      if (lease.closed) this.release(lease);
      else this.schedule(lease, delay);
    });
  }

  private release(lease: InterestLease, failures = 0): void {
    void this.client.releaseSourceViewportInterest(this.project, lease.view, lease.id).catch((error: unknown) => {
      // The host lease expires even if cleanup cannot be delivered.
      if (failures >= 3) return;
      const delay = retryDelay(error, failures + 1);
      if (delay !== undefined) setTimeout(() => this.release(lease, failures + 1), delay);
    });
  }

  clear(): void {
    const lease = this.lease;
    this.lease = undefined;
    if (!lease) return;
    lease.closed = true;
    clearTimeout(lease.timer);
    lease.pending = undefined;
    // Settling PUT before DELETE prevents late admission from restoring the lease.
    if (!lease.inFlight) this.release(lease);
  }
}
