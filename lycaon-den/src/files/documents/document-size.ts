import type { ChangeSet, Text } from "@codemirror/state";
import type { SourceEncoding } from "../../api/types.ts";
import type { EolKind } from "../../components/source/editor/eol.ts";

/** The host refuses a document whose saved file would exceed this; the window refuses the edit first. */
export const EDITABLE_DOCUMENT_MAX_BYTES = 4 * 1024 * 1024;

export type DocumentByteFormat = { encoding: SourceEncoding; eol: EolKind };

/** UTF-8 length of a string without allocating its bytes. */
export function utf8ByteLength(text: string): number {
  let bytes = 0;
  for (let index = 0; index < text.length; index++) {
    const unit = text.charCodeAt(index);
    if (unit < 0x80) bytes += 1;
    else if (unit < 0x800) bytes += 2;
    else if (unit >= 0xd800 && unit <= 0xdbff && index + 1 < text.length) {
      const next = text.charCodeAt(index + 1);
      if (next >= 0xdc00 && next <= 0xdfff) { bytes += 4; index++; continue; }
      bytes += 3;
    } else bytes += 3;
  }
  return bytes;
}

/** Bytes the host writes for a document: its text in the document's encoding with its line endings serialized. */
export function encodedByteLength(units: number, utf8: number, lines: number, format: DocumentByteFormat): number {
  const newlines = format.eol === "crlf" ? Math.max(0, lines - 1) : 0;
  switch (format.encoding) {
    case "utf-16le":
    case "utf-16be":
      return 2 * (units + newlines) + 2;
    case "utf-8-bom":
      return utf8 + newlines + 3;
    default:
      return utf8 + newlines;
  }
}

/** Admits edits under the host's cap, measuring only what changed. */
export class DocumentByteBudget {
  private measured?: { doc: Text; utf8: number };

  constructor(private readonly limit = EDITABLE_DOCUMENT_MAX_BYTES) {}

  /** The largest size any text of this length can encode to. */
  private ceiling(doc: Text, format: DocumentByteFormat): number {
    return encodedByteLength(doc.length, doc.length * 3, doc.lines, format);
  }

  admits(before: Text, after: Text, changes: ChangeSet, format: DocumentByteFormat): boolean {
    if (this.ceiling(after, format) <= this.limit) return true;
    let utf8: number;
    if (this.measured?.doc === before) {
      utf8 = this.measured.utf8;
      changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
        utf8 += utf8ByteLength(inserted.toString()) - utf8ByteLength(before.sliceString(fromA, toA));
      });
    } else {
      utf8 = utf8ByteLength(after.toString());
    }
    const fits = encodedByteLength(after.length, utf8, after.lines, format) <= this.limit;
    if (fits) this.measured = { doc: after, utf8 };
    return fits;
  }
}
