export type HistoryDirection = "older" | "newer";

type Failure = { key: string; attempts: number; input: number; retryAt: number };

/** Paging keeps reader direction independent of changes to resident geometry. */
export class TranscriptPageIntent {
  private direction: HistoryDirection | undefined;
  private input = 0;
  private failure: Failure | undefined;

  noteInput(direction: HistoryDirection): void {
    this.direction = direction;
    this.input += 1;
  }

  edge(older: boolean, newer: boolean): HistoryDirection | undefined {
    if (this.direction) return (this.direction === "older" ? older : newer) ? this.direction : undefined;
    if (older === newer) return undefined;
    this.direction = older ? "older" : "newer";
    return this.direction;
  }

  retryDelay(key: string, now: number): number | undefined {
    const failure = this.failure;
    if (!failure || failure.key !== key) return 0;
    if (failure.input === this.input) return undefined;
    return Math.max(0, failure.retryAt - now);
  }

  failed(key: string, now: number): void {
    const attempts = this.failure?.key === key ? this.failure.attempts + 1 : 1;
    this.failure = { key, attempts, input: this.input, retryAt: now + Math.min(8_000, 250 * 2 ** Math.min(attempts - 1, 5)) };
  }

  advanced(): void {
    this.failure = undefined;
  }
}
