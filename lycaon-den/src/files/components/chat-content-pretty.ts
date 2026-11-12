import { Decoration, EditorView } from "@codemirror/view";
import type { Extension } from "@codemirror/state";
import type { HostSecretRedactionMeta, RedactedSpan } from "../../api/types.ts";
import {
  redactionMarkModifier,
  redactionMarkTitle,
} from "../../chat/transcript/content/redaction-spans.ts";

/** Full retained text loaded so Pretty can lay it out in one editor. */
export interface ChatRetainedText {
  readonly text: string;
  readonly spans: readonly RedactedSpan[];
}

export function extractInlineRedactionSpans(
  redaction?: HostSecretRedactionMeta,
): RedactedSpan[] {
  if (!redaction?.spans?.length) return [];
  return redaction.spans.filter(
    (span) => span.field === "tool_result.content" || span.field === "content",
  );
}

/** UTF-16 offsets of each Unicode scalar in a text, built once per text. */
class RuneOffsets {
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
}

/** The UTF-16 offset of a rune offset in `text`. */
export function runeOffsetToUtf16(text: string, rune: number): number {
  return RuneOffsets.of(text).toUtf16(rune);
}

/** Whether redaction spans, in runes, all lie inside `text`. */
export function redactionSpansFit(text: string, spans: readonly RedactedSpan[]): boolean {
  const runes = RuneOffsets.of(text).runes;
  return spans.every((span) =>
    typeof span.start === "number" && typeof span.length === "number" &&
    span.start >= 0 && span.length >= 0 && span.start + span.length <= runes);
}

export function buildRawRedactionExtension(
  text: string,
  spans: readonly RedactedSpan[],
): Extension {
  if (!spans.length) return [];
  const offsets = RuneOffsets.of(text);
  const marks = spans.flatMap((span) => {
    const from = offsets.toUtf16(span.start);
    const to = offsets.toUtf16(span.start + span.length);
    return to > from
      ? [
          Decoration.mark({
            class: ["den-redaction-mark", redactionMarkModifier(span)]
              .filter(Boolean)
              .join(" "),
            attributes: { "data-tip": redactionMarkTitle(span) },
          }).range(from, to),
        ]
      : [];
  });
  return marks.length ? EditorView.decorations.of(Decoration.set(marks, true)) : [];
}
