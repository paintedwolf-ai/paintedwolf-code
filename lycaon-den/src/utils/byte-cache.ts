/** Least-recently-used retained values, bounded by both allocation estimate and count. */
export class ByteCache<K, V> {
  private readonly held = new Map<K, { value: V; bytes: number }>();
  private retained = 0;

  constructor(readonly maxBytes: number, readonly maxEntries: number) {
    if (!Number.isFinite(maxBytes) || maxBytes < 0 || !Number.isInteger(maxEntries) || maxEntries < 0) {
      throw new Error("Cache budgets must be finite and nonnegative.");
    }
  }

  get size(): number { return this.held.size; }
  get bytes(): number { return this.retained; }

  get(key: K): V | undefined {
    const entry = this.held.get(key);
    if (!entry) return undefined;
    this.held.delete(key);
    this.held.set(key, entry);
    return entry.value;
  }

  set(key: K, value: V, bytes: number): void {
    this.delete(key);
    if (!Number.isFinite(bytes) || bytes < 0) throw new Error("Cache entry size is invalid.");
    if (bytes > this.maxBytes || this.maxEntries === 0) return;
    this.held.set(key, { value, bytes });
    this.retained += bytes;
    while (this.retained > this.maxBytes || this.held.size > this.maxEntries) {
      this.delete(this.held.keys().next().value!);
    }
  }

  delete(key: K): boolean {
    const entry = this.held.get(key);
    if (!entry) return false;
    this.retained -= entry.bytes;
    return this.held.delete(key);
  }

  keys(): IterableIterator<K> { return this.held.keys(); }

  clear(): void {
    this.held.clear();
    this.retained = 0;
  }
}

/** Conservative UTF-16 and object estimate; no serialized copy is allocated. */
export function retainedValueBytes(value: unknown): number {
  const pending = [value];
  const seen = new Set<object>();
  let bytes = 0;
  while (pending.length) {
    const next = pending.pop();
    if (typeof next === "string") { bytes += next.length * 2 + 24; continue; }
    if (next === null || typeof next !== "object") { bytes += 8; continue; }
    if (seen.has(next)) continue;
    seen.add(next); bytes += 64;
    for (const [key, child] of Object.entries(next)) {
      bytes += key.length * 2 + 16;
      pending.push(child);
    }
  }
  return bytes;
}
