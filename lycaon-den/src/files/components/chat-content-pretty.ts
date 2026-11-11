import { Decoration, EditorView } from "@codemirror/view";
import type { Extension } from "@codemirror/state";
import type { HostSecretRedactionMeta, RedactedSpan } from "../../api/types.ts";
import {
  redactionMarkModifier,
  redactionMarkTitle,
} from "../../chat/transcript/content/redaction-spans.ts";
import { createRuneOffsetTable } from "../../components/source/pretty/pretty-limits.ts";
import {
  computePrettyNativeLayout,
  createPrettyNativeDecorationsExtension,
} from "../../components/source/pretty/pretty-native-layout.ts";
import type {
  PrettyNativeLayoutOptions,
  PrettyNativeLayoutResult,
  PrettyRuneOffsetTable,
} from "../../components/source/pretty/pretty-types.ts";

export interface ChatPrettyProjectionState {
  readonly rawText: string;
  readonly layout: PrettyNativeLayoutResult;
  readonly runeTable: PrettyRuneOffsetTable;
  readonly redactionExtension: Extension;
  readonly prettyExtension: Extension;
}

export function extractInlineRedactionSpans(
  redaction?: HostSecretRedactionMeta,
): RedactedSpan[] {
  if (!redaction?.spans?.length) return [];
  return redaction.spans.filter(
    (span) => span.field === "tool_result.content" || span.field === "content",
  );
}

export function buildRawRedactionExtension(
  text: string,
  spans: readonly RedactedSpan[],
): Extension {
  if (!spans.length) return [];
  const runeTable = createRuneOffsetTable(text);
  const marks = spans.flatMap((span) => {
    const from = runeTable.runeToUtf16Offset(span.start);
    const to = runeTable.runeToUtf16Offset(span.start + span.length);
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

export function buildChatPrettyProjectionState(
  rawText: string,
  spans: readonly RedactedSpan[],
  options: PrettyNativeLayoutOptions,
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
  const layout = computePrettyNativeLayout(rawText, options);
  if (layout.outcome !== "formatted") {
    return null;
  }
  return {
    rawText,
    layout,
    runeTable,
    redactionExtension: buildRawRedactionExtension(rawText, spans),
    prettyExtension: createPrettyNativeDecorationsExtension({
      enabled: true,
      ...(options.path !== undefined ? { path: options.path } : {}),
      ...(options.languageHint !== undefined
        ? { languageHint: options.languageHint }
        : {}),
      ...(options.limits !== undefined ? { limits: options.limits } : {}),
    }),
  };
}

export function mapRuneOffsetToDisplay(
  state: ChatPrettyProjectionState | null | undefined,
  runeOffset: number,
  rawTextFallback?: string,
): number {
  if (state) {
    return state.runeTable.runeToUtf16Offset(runeOffset);
  }
  if (!rawTextFallback) return Math.max(0, runeOffset);
  return createRuneOffsetTable(rawTextFallback).runeToUtf16Offset(runeOffset);
}
