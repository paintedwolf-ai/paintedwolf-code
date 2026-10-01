export class DocumentCapacityError extends Error {}

export type ResidencyPriority = "foreground" | "background";

type Entry = {
  key: string;
  bytes: number;
  retainedBytes?: number;
  pins: number;
  touched: number;
  protected: boolean;
  evict?: () => Promise<boolean>;
  releasing: boolean;
  blocked?: boolean;
};

export type DocumentReservation = {
  identify(key: string): void;
  expand(bytes: number): Promise<void>;
  retain(bytes: number, evict: () => Promise<boolean>): void;
  release(): void;
};

type Demand = {
  key: string;
  bytes: number;
  priority: ResidencyPriority;
  current: () => boolean;
  resolve: (reservation: DocumentReservation | null) => void;
  reject: (error: unknown) => void;
};

/** Accounts for bodies throughout acquisition, retention and asynchronous disposal. */
export class DocumentResidency {
  private entries = new Map<string, Entry>();
  private queue: Demand[] = [];
  private running = false;
  private wakePending = false;
  private clock = 0;
  private epoch = 0;
  private acquisitions = 0;
  private background = 0;
  private protections = new Map<string, Set<symbol>>();
  private readonly defaultProtection = Symbol();

  constructor(private budget: number, private readonly changed = () => {}) {}

  get snapshot() {
    return {
      budget: this.budget,
      bytes: [...this.entries.values()].reduce((sum, entry) => sum + entry.bytes, 0),
      documents: this.entries.size,
      queued: this.queue.length,
      acquiring: this.acquisitions,
      releasing: [...this.entries.values()].filter(entry => entry.releasing).length,
    };
  }

  reset(): void {
    this.epoch++;
    this.running = false;
    this.wakePending = false;
    this.acquisitions = 0;
    this.background = 0;
    for (const demand of this.queue) demand.resolve(null);
    this.queue = [];
    this.entries.clear();
    this.protections.clear();
  }

  setBudget(bytes: number): void {
    this.budget = Math.max(0, bytes);
    this.wake();
  }

  protect(key: string, protectedValue: boolean, token = this.defaultProtection): void {
    const tokens = this.protections.get(key) ?? new Set<symbol>();
    if (protectedValue) tokens.add(token);
    else tokens.delete(token);
    if (tokens.size) this.protections.set(key, tokens);
    else this.protections.delete(key);
    const entry = this.entries.get(key);
    if (entry) {
      entry.protected = tokens.size > 0;
      if (protectedValue) entry.touched = ++this.clock;
    }
    this.wake();
  }

  resize(key: string, bytes: number, canRelease = true): void {
    const entry = this.entries.get(key);
    if (entry) {
      entry.retainedBytes = Math.max(0, bytes);
      entry.bytes = entry.pins ? Math.max(entry.bytes, entry.retainedBytes) : entry.retainedBytes;
      entry.blocked = !canRelease;
    }
    this.wake();
  }

  isProtected(key: string): boolean { return this.protections.has(key); }

  acquire(key: string, bytes: number, priority: ResidencyPriority, current = () => true): Promise<DocumentReservation | null> {
    return new Promise((resolve, reject) => {
      this.queue.push({ key, bytes, priority, current, resolve, reject });
      this.wake();
    });
  }

  /** A closed payload remains charged until its asynchronous release completes. */
  forget(key: string): void {
    const entry = this.entries.get(key);
    if (entry && !entry.pins && !entry.releasing) this.entries.delete(key);
    this.wake();
  }

  wake(): void {
    this.changed();
    if (this.running) { this.wakePending = true; return; }
    this.running = true;
    const epoch = this.epoch;
    queueMicrotask(() => { void this.drain(epoch).finally(() => {
      if (epoch !== this.epoch) return;
      this.running = false;
      this.changed();
      if (this.wakePending) { this.wakePending = false; this.wake(); }
    }); });
  }

  private reservation(entry: Entry, priority: ResidencyPriority): DocumentReservation {
    entry.pins++;
    entry.touched = ++this.clock;
    this.acquisitions++;
    if (priority === "background") this.background++;
    let released = false;
    const epoch = this.epoch;
    return {
      expand: async bytes => {
        if (released || epoch !== this.epoch) throw new Error("The document reservation has been released.");
        const additional = Math.max(0, bytes - entry.bytes);
        // Charge before awaiting disposal so concurrent expansions see each other.
        entry.bytes += additional;
        if (!(await this.reclaim(0))) {
          entry.bytes -= additional;
          throw new DocumentCapacityError("Document memory capacity is exhausted. Close another file or window and retry.");
        }
      },
      identify: key => {
        if (released || epoch !== this.epoch) throw new Error("The document reservation has been released.");
        if (key === entry.key) return;
        const existing = this.entries.get(key);
        if (existing) {
          entry.pins--;
          existing.pins++;
          existing.bytes = Math.max(existing.bytes, entry.bytes);
          if (!entry.pins) this.entries.delete(entry.key);
          entry = existing;
          return;
        }
        this.entries.delete(entry.key);
        entry.key = key;
        entry.protected = this.protections.has(key);
        this.entries.set(key, entry);
      },
      retain: (bytes, evict) => {
        if (released || epoch !== this.epoch) throw new Error("The document reservation has been released.");
        entry.retainedBytes = Math.max(0, bytes);
        entry.bytes = Math.max(entry.bytes, entry.retainedBytes);
        entry.evict = evict;
      },
      release: () => {
        if (released || epoch !== this.epoch) return;
        released = true;
        entry.pins--;
        this.acquisitions--;
        if (priority === "background") this.background--;
        if (!entry.pins && !entry.evict) this.entries.delete(entry.key);
        else if (!entry.pins && entry.retainedBytes !== undefined) entry.bytes = entry.retainedBytes;
        this.wake();
      },
    };
  }

  private async reclaim(required: number): Promise<boolean> {
    const candidates = [...this.entries.values()]
      .filter(entry => !entry.pins && !entry.protected && !entry.releasing && !entry.blocked && entry.evict)
      .sort((a, b) => a.touched - b.touched);
    for (const entry of candidates) {
      if (this.snapshot.bytes + required <= this.budget) return true;
      const evict = entry.evict;
      if (entry.pins || entry.protected || !evict) continue;
      entry.releasing = true;
      this.changed();
      try {
        if (await evict() && this.entries.get(entry.key) === entry) this.entries.delete(entry.key);
      } catch {
        entry.blocked = true;
        // A failed preservation keeps its reservation and the document intact.
      } finally {
        entry.releasing = false;
      }
    }
    return this.snapshot.bytes + required <= this.budget;
  }

  private async drain(epoch: number): Promise<void> {
    await this.reclaim(0);
    if (epoch !== this.epoch) return;
    this.queue.sort((a, b) => Number(a.priority === "background") - Number(b.priority === "background"));
    for (let index = 0; epoch === this.epoch && index < this.queue.length;) {
      const demand = this.queue[index];
      if (!demand) break;
      if (!demand.current()) {
        this.queue.splice(index, 1);
        demand.resolve(null);
        continue;
      }
      if (this.acquisitions >= 2 || (demand.priority === "background" && this.background >= 1)) { index++; continue; }
      const existing = this.entries.get(demand.key);
      if (existing?.releasing) { index++; continue; }
      if (demand.bytes > this.budget && !existing) {
        this.queue.splice(index, 1);
        demand.reject(new DocumentCapacityError("Document memory capacity is exhausted. Close another file or window and retry."));
        continue;
      }
      if (!existing && !(await this.reclaim(demand.bytes))) {
        if (this.acquisitions) { index++; continue; }
        this.queue.splice(index, 1);
        if (demand.priority === "background") demand.resolve(null);
        else demand.reject(new DocumentCapacityError("Document memory capacity is exhausted. Close another file or window and retry."));
        continue;
      }
      if (epoch !== this.epoch) return;
      if (!demand.current()) continue;
      const entry = existing ?? { key: demand.key, bytes: demand.bytes, pins: 0,
        touched: ++this.clock, protected: this.protections.has(demand.key), releasing: false };
      this.entries.set(demand.key, entry);
      this.queue.splice(index, 1);
      demand.resolve(this.reservation(entry, demand.priority));
    }
  }
}

export const DOCUMENT_MEMORY_BUDGET = 256 * 1024 * 1024;
export const DOCUMENT_OPEN_RESERVATION = 64 * 1024 * 1024;
/** The largest working file the host serves as an editable document (`project.SourceReadMaxBytes`). */
export const EDITABLE_SOURCE_MAX_BYTES = 4 * 1024 * 1024;
/** Base64 CRDT state for a limit-sized file: one character per six bits plus item framing. */
const LIMIT_ENCODED_UNITS = Math.ceil((EDITABLE_SOURCE_MAX_BYTES * 4) / 3) + 65536;
/** A window always admits a switch between two limit-sized files: the displayed one and its replacement. */
export const DOCUMENT_WINDOW_FLOOR = 2 * documentAdmissionBytes(EDITABLE_SOURCE_MAX_BYTES, LIMIT_ENCODED_UNITS);
export const documentResidency = new DocumentResidency(DOCUMENT_MEMORY_BUDGET);

/** Windows share the application budget evenly, but never below what one tab switch needs. */
export function windowDocumentBudget(windows: number): number {
  return Math.max(DOCUMENT_WINDOW_FLOOR, Math.floor(DOCUMENT_MEMORY_BUDGET / Math.max(1, windows)));
}

type DocumentResourceAddress = {
  rootId: string; path: string; documentId?: string | null; fileId?: string | null; jobId?: string;
} | { documentId: string; rootId?: string; path?: string; fileId?: string | null; jobId?: string };

export function documentResourceKey(projectId: string, address: DocumentResourceAddress): string {
  if (address.documentId) return `${projectId}\0document\0${address.documentId}`;
  if (address.fileId && !address.jobId) return `${projectId}\0file\0${address.fileId}`;
  return `${projectId}\0${address.rootId}\0${address.jobId ?? ""}\0${address.path}`;
}

/** Leave room for the displayed document and a preparing replacement in each window. */
export function documentOpeningReservation(): number {
  return Math.max(8 * 1024 * 1024, Math.min(DOCUMENT_OPEN_RESERVATION, Math.floor(documentResidency.snapshot.budget / 2)));
}

/** Two CRDTs and editor text coexist with the larger wire or recovery payload. */
export function documentAdmissionBytes(sizeBytes: number, encodedUnits: number, recovery = 0): number {
  return 1024 * 1024 + sizeBytes * 14 + Math.max(encodedUnits * 2, recovery);
}
