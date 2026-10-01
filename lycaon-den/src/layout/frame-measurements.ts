/** All requested boxes are measured before callbacks change layout. */
export class FrameMeasurements<Key, Measurement> {
  private readonly pending = new Map<Key, object>();
  private frame: number | undefined;

  constructor(
    private readonly read: (key: Key) => Measurement,
    private readonly write: (key: Key, measurement: Measurement) => void,
  ) {}

  request(key: Key): void {
    this.pending.set(key, {});
    if (this.frame !== undefined) return;
    this.frame = requestAnimationFrame(() => this.flush());
  }

  /** Reads one key now, for a caller that runs its own measure phase. */
  measure(key: Key): Measurement {
    return this.read(key);
  }

  cancel(key: Key): void {
    this.pending.delete(key);
    if (this.pending.size === 0) this.cancelFrame();
  }

  clear(): void {
    this.pending.clear();
    this.cancelFrame();
  }

  private cancelFrame(): void {
    if (this.frame !== undefined) cancelAnimationFrame(this.frame);
    this.frame = undefined;
  }

  private flush(): void {
    this.frame = undefined;
    const measured = [...this.pending].map(([key, token]) => ({
      key, token, value: this.read(key),
    }));
    for (const { key, token, value } of measured) {
      if (this.pending.get(key) !== token) continue;
      this.pending.delete(key);
      this.write(key, value);
    }
  }
}
