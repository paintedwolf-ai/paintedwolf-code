import { SECRET_SPAN_CLASSES } from "../secrets/secret-span-decorations.ts";
import { Text } from "@codemirror/state";
import { secretSpanMarks } from "../secrets/secret-span-model.ts";
import { tags } from "@lezer/highlight";
import { denHighlightStyle } from "../editor/codemirror-highlight.generated.ts";
import type { SourceReaderRow } from "../../../api/types.ts";

const syntaxTags = { keyword: tags.keyword, string: tags.string, number: tags.number, comment: tags.comment,
  function: tags.function(tags.variableName), operator: tags.operator, property: tags.propertyName,
  constant: tags.atom, variable: tags.variableName, type: tags.typeName };

export type ReaderMark = { from: number; to: number; class: string; title?: string };
export function readerSecretSpans(row: SourceReaderRow) {
  if (!row.secret_screen?.spans?.length) return [];
  return secretSpanMarks(row.secret_screen, Text.of(row.text.split("\n")));
}

/** Independent ranges keep change backgrounds continuous across syntax boundaries. */
export function readerMarks(row: SourceReaderRow, match?: { from: number; to: number }): ReaderMark[] {
  const marks: ReaderMark[] = row.changed.map(span => ({ ...span, class: row.kind === "delete" ? "cm-deletedText" : "cm-changedText" }));
  if (match) marks.push({ ...match, class: "den-source-reader__find-active" });
  for (const span of readerSecretSpans(row)) marks.push({ from: span.from, to: span.to,
    class: SECRET_SPAN_CLASSES[span.state], title: span.ruleTitle || span.ruleId });
  for (const token of row.syntax ?? []) marks.push({ from: token.from, to: token.to, class: denHighlightStyle.style([syntaxTags[token.kind]]) ?? "" });
  return marks.map(mark => ({ ...mark, from: Math.max(0, mark.from), to: Math.min(row.text.length, mark.to) }))
    .filter(mark => mark.class && mark.to > mark.from);
}
