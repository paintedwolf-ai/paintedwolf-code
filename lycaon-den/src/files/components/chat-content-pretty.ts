import { Decoration, EditorView } from "@codemirror/view";
import type { Extension } from "@codemirror/state";
import type { HostSecretRedactionMeta, RedactedSpan } from "../../api/types.ts";
import { redactionMarkModifier, redactionMarkTitle } from "../../chat/transcript/content/redaction-spans.ts";
import { lineGutterSource } from "../../components/source/annotations/line-gutter.ts";
import {
  createRuneOffsetTable,
  displayOffsetToSourceLineCol,
  mapRuneSpansToDisplay,
  sourceLineColToDisplay,
  sourceToDisplayOffset,
} from "../../components/source/pretty/pretty-map.ts";
import { formatPrettyProjection } from "../../components/source/pretty/pretty-segmenter.ts";
import type {
  PrettyFormatOptions,
  PrettyProjectionResult,
  PrettyRuneOffsetTable,
  PrettySourceMap,
} from "../../components/source/pretty/pretty-types.ts";

export interface ChatPrettyProjectionState {
  readonly rawText: string;
  readonly displayText: string;
  readonly projection: PrettyProjectionResult;
  readonly runeTable: PrettyRuneOffsetTable;
  readonly redactionExtension: Extension;
  readonly gutterExtension: Extension;
}

export function extractInlineRedactionSpans(redaction?: HostSecretRedactionMeta): RedactedSpan[] {
  if (!redaction?.spans?.length) return [];
  return redaction.spans.filter(
    (span) => span.field === "tool_result.content" || span.field === "content",
  );
}

export function buildRawRedactionExtension(text: string, spans: readonly RedactedSpan[]): Extension {
  if (!spans.length) return [];
  const offsets = [0];
  for (const rune of text) offsets.push((offsets.at(-1) ?? 0) + rune.length);
  const marks = spans.flatMap((span) => {
    const from = offsets[span.start];
    const to = offsets[span.start + span.length];
    return from !== undefined && to !== undefined && to > from
      ? [
          Decoration.mark({
            class: ["den-redaction-mark", redactionMarkModifier(span)].filter(Boolean).join(" "),
            attributes: { "data-tip": redactionMarkTitle(span) },
          }).range(from, to),
        ]
      : [];
  });
  return EditorView.decorations.of(Decoration.set(marks, true));
}

export function buildPrettyGutterExtension(sourceMap: PrettySourceMap): Extension {
  return lineGutterSource.of((_state, line) => {
    const info = sourceMap.displayLines[line - 1];
    if (!info) return { label: String(line) };
    return {
      label: info.isContinuation ? "" : String(info.sourceLine),
    };
  });
}

export function buildChatPrettyProjectionState(
  rawText: string,
  spans: readonly RedactedSpan[],
  options: PrettyFormatOptions,
): ChatPrettyProjectionState | null {
  const runeTable = createRuneOffsetTable(rawText);
  for (const span of spans) {
    if (
      typeof span.start !== "number" ||
      typeof span.length !== "number" ||
      span.start < 0 ||
      span.length < 0 ||
      span.start + span.length > runeTable.runeCount
    ) {
      return null;
    }
  }
  const projection = formatPrettyProjection(rawText, options);
  if (projection.outcome !== "formatted") {
    return null;
  }
  const nonEmptySpans = spans.filter((span) => span.length > 0);
  const projectedSpans = mapRuneSpansToDisplay(
    projection.sourceMap,
    runeTable,
    nonEmptySpans,
  );
  if (projectedSpans.length < nonEmptySpans.length) {
    return null;
  }
  const marks = projectedSpans.flatMap((projected) => {
    const span = projected.span;
    if (!span || projected.dispTo <= projected.dispFrom) return [];
    return [
      Decoration.mark({
        class: ["den-redaction-mark", redactionMarkModifier(span)].filter(Boolean).join(" "),
        attributes: { "data-tip": redactionMarkTitle(span) },
      }).range(projected.dispFrom, projected.dispTo),
    ];
  });
  return {
    rawText,
    displayText: projection.displayText,
    projection,
    runeTable,
    redactionExtension: marks.length ? EditorView.decorations.of(Decoration.set(marks, true)) : [],
    gutterExtension: buildPrettyGutterExtension(projection.sourceMap),
  };
}

export function mapRuneOffsetToDisplay(
  state: ChatPrettyProjectionState | null | undefined,
  runeOffset: number,
  rawTextFallback?: string,
): number {
  if (!state) {
    if (!rawTextFallback) return Math.max(0, runeOffset);
    const runes = Array.from(rawTextFallback);
    return runes.slice(0, Math.max(0, Math.min(runeOffset, runes.length))).join("").length;
  }
  const clampedRune = Math.max(0, Math.min(runeOffset, state.runeTable.runeCount));
  const rawUtf16 = state.runeTable.runeToUtf16[clampedRune] ?? 0;
  return sourceToDisplayOffset(state.projection.sourceMap, rawUtf16, "right");
}

export { displayOffsetToSourceLineCol, sourceLineColToDisplay };
