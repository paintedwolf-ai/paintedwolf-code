import type { HostSecretRedactionMeta, Message, RedactedSpan } from "../../../api/types.ts";

/**
 * Redaction provenance comes from the host's span list. Presence of the metadata
 * is authoritative; text that merely quotes `[REDACTED]` is not a redaction.
 */

/** Field path the host reports whole-message content spans against. */
export const REDACTION_FIELD_CONTENT = "content";

/** Field prefix for args restated on a tool-result row. */
export const REDACTION_FIELD_TOOL_RESULT_ARGS = "tool_result.tool_args";

/** Field prefix for args on the assistant row that made the call. */
export function toolCallArgsRedactionField(callIndex: number): string {
  return `tool_calls.${callIndex}.args`;
}

/** Locates argument spans on the row that supplied them. */
export type ToolArgsRedaction = {
  meta: HostSecretRedactionMeta;
  fieldPrefix: string;
};

/** Full field path for one argument key under a prefix. */
export function argRedactionField(fieldPrefix: string, key: string): string {
  return `${fieldPrefix}.${key}`;
}

/** One run of a redacted string: either literal text, or a mark. */
export type RedactionSegment =
  | { kind: "text"; text: string }
  | { kind: "mark"; span: RedactedSpan; text: string };

function metaOf(message: Pick<Message, "host_secret_redaction"> | undefined): HostSecretRedactionMeta | undefined {
  return message?.host_secret_redaction ?? undefined;
}

/** Spans the host recorded against one field of a message. */
export function spansForField(
  message: Pick<Message, "host_secret_redaction"> | undefined,
  field: string,
): RedactedSpan[] {
  const meta = metaOf(message);
  if (!meta?.spans?.length) return [];
  return meta.spans
    .filter((span) => span.field === field)
    .slice()
    .sort((a, b) => a.start - b.start);
}

/** Every span in a message copy, in field then offset order. */
export function allSpans(message: Pick<Message, "host_secret_redaction"> | undefined): RedactedSpan[] {
  const meta = metaOf(message);
  if (!meta?.spans?.length) return [];
  return meta.spans
    .slice()
    .sort((a, b) => (a.field === b.field ? a.start - b.start : a.field.localeCompare(b.field)));
}

/**
 * Splits text into literal runs and marks. Offsets are rune indices, so the walk
 * is over code points; UTF-16 units would land mid-glyph after an emoji.
 */
export function segmentBySpans(text: string, spans: RedactedSpan[]): RedactionSegment[] {
  if (!spans.length) return text ? [{ kind: "text", text }] : [];
  const points = Array.from(text);
  const out: RedactionSegment[] = [];
  let cursor = 0;
  for (const span of spans) {
    const start = Math.max(0, Math.min(span.start, points.length));
    const end = Math.max(start, Math.min(start + span.length, points.length));
    if (start > cursor) out.push({ kind: "text", text: points.slice(cursor, start).join("") });
    out.push({ kind: "mark", span, text: points.slice(start, end).join("") });
    cursor = end;
  }
  if (cursor < points.length) out.push({ kind: "text", text: points.slice(cursor).join("") });
  return out;
}

/** Distinct rule titles across a message's spans, for the card chip. */
export function redactionRuleTitles(message: Pick<Message, "host_secret_redaction"> | undefined): string[] {
  const seen = new Set<string>();
  for (const span of allSpans(message)) {
    const title = span.rule_title?.trim();
    if (title) seen.add(title);
  }
  return [...seen];
}

export const REDACTION_MARK_CLASS = "den-redaction-mark";
export const REDACTION_MASK_CLASS = "den-redaction-mark--mask";
export const REDACTION_REFERENCE_CLASS = "den-redaction-mark--reference";

/** Modifier class for a mark that is not a removed secret. */
export function redactionMarkModifier(span: RedactedSpan): string | undefined {
  if (span.kind === "observer_mask") return REDACTION_MASK_CLASS;
  if (span.kind === "managed_reference") return REDACTION_REFERENCE_CLASS;
  return undefined;
}

/** What one mark tells a reader about its replacement. */
export function redactionMarkTitle(span: RedactedSpan): string {
  if (span.kind === "observer_mask") return "Not shown to observers";
  if (span.kind === "managed_reference") {
    return "Protected secret shown as its reference — the source still holds the value";
  }
  return [span.rule_title?.trim(), "removed by this host"].filter(Boolean).join(" — ");
}

/** Summary a card header shows: how many replacements, and what named them. */
export interface RedactionSummary {
  secrets: number;
  references: number;
  masked: number;
  titles: string[];
}

export function redactionSummary(
  message: Pick<Message, "host_secret_redaction"> | undefined,
): RedactionSummary | undefined {
  const spans = allSpans(message);
  if (!spans.length) return undefined;
  let secrets = 0;
  let references = 0;
  let masked = 0;
  for (const span of spans) {
    if (span.kind === "observer_mask") masked += 1;
    else if (span.kind === "managed_reference") references += 1;
    else secrets += 1;
  }
  return { secrets, references, masked, titles: redactionRuleTitles(message) };
}
