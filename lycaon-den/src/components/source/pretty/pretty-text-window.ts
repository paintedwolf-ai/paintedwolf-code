import type { Text } from "@codemirror/state";
import type { PrettyTokenSource } from "./pretty-tokens.ts";

const CHUNK = 8192;

/**
 * Character access over a document by absolute position, read in chunks,
 * with its syntax tokens where a rule reads them.
 */
export class TextWindow {
  private from = 0;
  private text = "";
  /** Characters read from the document, for work bounds. */
  read = 0;

  constructor(readonly doc: Text, readonly tokens: PrettyTokenSource | null = null) {}

  at(pos: number): number {
    if (pos < this.from || pos >= this.from + this.text.length) this.load(pos);
    return this.text.charCodeAt(pos - this.from);
  }

  slice(from: number, to: number): string {
    if (from >= this.from && to <= this.from + this.text.length) {
      return this.text.slice(from - this.from, to - this.from);
    }
    return this.doc.sliceString(from, to);
  }

  private load(pos: number): void {
    const from = Math.max(0, pos - 256);
    this.text = this.doc.sliceString(from, Math.min(this.doc.length, from + CHUNK));
    this.from = from;
    this.read += this.text.length;
  }
}
