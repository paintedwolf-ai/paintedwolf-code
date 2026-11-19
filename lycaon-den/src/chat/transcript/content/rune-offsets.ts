/** UTF-16 offsets of each Unicode scalar in a text, built once per text. */
export class RuneOffsets {
  private static last: RuneOffsets | null = null;

  static of(text: string): RuneOffsets {
    if (RuneOffsets.last?.text !== text) RuneOffsets.last = new RuneOffsets(text);
    return RuneOffsets.last;
  }

  private readonly offsets: Int32Array;

  private constructor(readonly text: string) {
    const offsets: number[] = [];
    for (let i = 0; i < text.length; i++) {
      offsets.push(i);
      const code = text.charCodeAt(i);
      if (code >= 0xd800 && code <= 0xdbff && i + 1 < text.length) {
        const low = text.charCodeAt(i + 1);
        if (low >= 0xdc00 && low <= 0xdfff) i += 1;
      }
    }
    offsets.push(text.length);
    this.offsets = Int32Array.from(offsets);
  }

  get runes(): number {
    return this.offsets.length - 1;
  }

  toUtf16(rune: number): number {
    if (rune <= 0) return 0;
    if (rune >= this.runes) return this.text.length;
    return this.offsets[rune]!;
  }

  /** Rune at or before a UTF-16 position, including positions inside a surrogate pair. */
  toRune(position: number): number {
    let lo = 0, hi = this.runes;
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2);
      if (this.offsets[mid]! <= position) lo = mid;
      else hi = mid - 1;
    }
    return lo;
  }
}
