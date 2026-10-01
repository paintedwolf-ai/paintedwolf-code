import { DocumentCapacityError } from "./document-residency.ts";
import { DocumentStorageError } from "./document-outbox.ts";
import { LycaonApiError, isApiErrorCode } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";

export type FileDocumentOpening = (
  | { status: "opening" }
  | { status: "reconnecting"; message: string }
  | { status: "error"; message: string }) & { editingBlock?: "storage" | "capacity" };

export type DocumentOpeningTarget<T> = { key: string; identity: string; instance: object; value: T; background?: boolean };
type Attempt<T> = { target: DocumentOpeningTarget<T>; incarnation: string; failures: number; timer?: ReturnType<typeof setTimeout>; running: boolean; failed: boolean };

function canRetry(error: unknown): boolean {
  if (error instanceof DOMException && error.name === "TimeoutError") return true;
  if (error instanceof DocumentStorageError) return error.retryable;
  if (error instanceof BackendTransportError) return true;
  if (!(error instanceof LycaonApiError)) return false;
  return error.retryable ?? isApiErrorCode(error, ["rate_limited"]);
}

/** Manages initial joins and their recovery until the addressed file leaves the view. */
export class FileDocumentOpeningController<T, D> {
  private attempts = new Map<string, Attempt<T>>();
  private connection: unknown;
  private connectionRevision = 0;
  private reachable = false;
  private disposed = false;
  private running = 0;
  private backgroundRunning = 0;

  constructor(private readonly options: {
    open: (target: T, incarnation: string, background: boolean, current: () => boolean) => Promise<D | null>;
    current: (target: DocumentOpeningTarget<T>) => boolean;
    state: (target: T, state: FileDocumentOpening | undefined) => void;
    accept: (target: T, document: D) => void;
    release: (document: D) => Promise<unknown>;
  }) {}

  get runningKeys(): string[] {
    return [...this.attempts.values()].filter(attempt => attempt.running).map(attempt => attempt.target.key);
  }

  reconcile(connection: unknown, reachable: boolean, targets: readonly DocumentOpeningTarget<T>[]): void {
    if (this.disposed) return;
    const changed = this.connection !== connection || this.reachable !== reachable;
    if (changed) this.connectionRevision++;
    this.connection = connection;
    this.reachable = reachable;
    const wanted = new Map(targets.map((target) => [target.key, target]));
    for (const [key, attempt] of this.attempts) {
      const target = wanted.get(key);
      if (target?.identity !== attempt.target.identity || target?.instance !== attempt.target.instance) {
        clearTimeout(attempt.timer);
        this.attempts.delete(key);
      } else if (changed && !attempt.running) {
        clearTimeout(attempt.timer);
        attempt.timer = undefined;
        attempt.failures = 0;
        attempt.failed = false;
      }
      if (target && this.attempts.get(key) === attempt) attempt.target = target;
    }
    for (const target of targets) {
      if (this.attempts.has(target.key)) continue;
      const attempt = { target, incarnation: crypto.randomUUID(), failures: 0, running: false, failed: false };
      this.attempts.set(target.key, attempt);
      this.options.state(target.value, { status: "opening" });
    }
    this.pump();
  }

  dispose(): void {
    this.disposed = true;
    for (const attempt of this.attempts.values()) clearTimeout(attempt.timer);
    this.attempts.clear();
  }

  retry(key: string): void {
    const attempt = this.attempts.get(key);
    if (!attempt || attempt.running || !this.current(attempt)) return;
    clearTimeout(attempt.timer);
    attempt.timer = undefined;
    attempt.failures = 0;
    attempt.failed = false;
    this.pump();
  }

  private pump(): void {
    const queued = [...this.attempts.values()].filter(attempt =>
      this.current(attempt) && !attempt.running && !attempt.failed && !attempt.timer);
    queued.sort((a, b) => Number(!!a.target.background) - Number(!!b.target.background));
    for (const attempt of queued) {
      if (this.running >= 2) break;
      if (attempt.target.background && this.backgroundRunning >= 1) continue;
      this.start(attempt);
    }
  }

  private current(attempt: Attempt<T>): boolean {
    return !this.disposed && this.attempts.get(attempt.target.key) === attempt && this.options.current(attempt.target);
  }

  private start(attempt: Attempt<T>): void {
    if (!this.current(attempt) || attempt.running || attempt.failed) return;
    if (!this.connection || !this.reachable) {
      this.options.state(attempt.target.value, { status: "reconnecting", message: "Waiting for the connection to open this file for editing." });
      return;
    }
    attempt.running = true;
    this.running++;
    const background = !!attempt.target.background;
    if (background) this.backgroundRunning++;
    const connectionRevision = this.connectionRevision;
    if (!attempt.failures) this.options.state(attempt.target.value, { status: "opening" });
    void this.options.open(attempt.target.value, attempt.incarnation, background, () => this.current(attempt)).then(async (document) => {
      if (!this.current(attempt)) {
        if (document) await this.options.release(document);
        return;
      }
      if (!document) throw new Error("The file could not be opened for editing.");
      this.options.accept(attempt.target.value, document);
      // Keep the settled attempt until reconciliation sees the admitted replica.
      attempt.failed = true;
    }).catch((error: unknown) => {
      if (!this.current(attempt)) return;
      const message = error instanceof Error ? error.message : "The file could not be opened for editing.";
      const block = error instanceof DocumentCapacityError ? { editingBlock: "capacity" as const }
        : error instanceof DocumentStorageError ? { editingBlock: "storage" as const } : {};
      if (connectionRevision === this.connectionRevision && this.connection && this.reachable && !canRetry(error)) {
        attempt.failed = true;
        this.options.state(attempt.target.value, { status: "error", message, ...block });
        return;
      }
      attempt.failures++;
      this.options.state(attempt.target.value, { status: "reconnecting", message, ...block });
      attempt.timer = setTimeout(() => {
        attempt.timer = undefined;
        this.pump();
      }, Math.min(1000 * 2 ** Math.min(attempt.failures - 1, 6), 60_000));
    }).finally(() => {
      attempt.running = false;
      this.running--;
      if (background) this.backgroundRunning--;
      this.pump();
    });
  }
}
