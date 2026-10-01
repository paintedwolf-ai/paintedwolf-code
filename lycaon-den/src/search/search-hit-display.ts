import type { SearchHit } from "../api/types.ts";
import { formatSentenceCase } from "../format/format-sentence-case.ts";
import { highlightIndexes } from "../files/tree/file-inventory.ts";

export type SearchHitPreviewFormat = "prose" | "code" | "json";

export type SearchHitDisplay = {
  title: string;
  /** UTF-16 indexes of title the host says the query matched. */
  titleMatches?: readonly number[];
  context?: string;
  preview?: { format: SearchHitPreviewFormat; text: string };
  kindLabel: string;
  openLabel: string;
  pivotPath?: string;
  openPath?: { path: string; line?: number };
  openUrl?: string;
};

export function softTitle(text: string, max = 220): string {
  const title = text.replace(/\s+/g, " ").trim();
  if (title.length <= max) return title;
  return `${title.slice(0, max).trimEnd()}…`;
}

function formatJson(text: string): string | undefined {
  const value = text.trim();
  if (!value.startsWith("{") && !value.startsWith("[")) return undefined;
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    // Deep JSON can parse but exceed the formatter's stack limit.
    return undefined;
  }
}

function unescapeJsonish(text: string): string {
  return text
    .replace(/\\n/g, "\n")
    .replace(/\\t/g, "\t")
    .replace(/\\"/g, '"')
    .replace(/\\u003c/gi, "<")
    .replace(/\\u003e/gi, ">");
}

function kindDisplayLabel(hit: SearchHit): string {
  const kind = hit.hit_kind.trim().toLowerCase();
  switch (kind) {
    case "symbol":
      return hit.symbol_kind ? formatSentenceCase(hit.symbol_kind) : "Symbol";
    case "finding":
      return "Finding";
    case "code":
      return "Code";
    case "file":
      return "File";
    case "message":
      return "Message";
    case "evidence":
      return "Evidence";
    case "claim":
      return "Claim";
    case "tool":
      return "Tool";
    case "web":
      return "Web";
    case "artifact":
      return "Artifact";
    case "outcome":
      return "Outcome";
    default:
      return kind ? formatSentenceCase(kind) : "Hit";
  }
}

function openLabelFor(hit: SearchHit): string {
  const kind = hit.hit_kind.toLowerCase();
  if (
    kind === "code" ||
    kind === "symbol" ||
    kind === "file" ||
    kind === "finding" ||
    kind === "web" ||
    kind === "artifact"
  ) {
    return "Open";
  }
  if (
    kind === "evidence" ||
    kind === "claim" ||
    kind === "message" ||
    kind === "tool" ||
    hit.session_id
  ) {
    return "Open in chat";
  }
  return "Open";
}

function previewFromText(
  text: string,
  format: SearchHitPreviewFormat = "prose",
): SearchHitDisplay["preview"] {
  const value = text.trim();
  if (!value) return undefined;
  const json = formatJson(value);
  if (json !== undefined) {
    return { format: "json", text: json };
  }
  if (value.includes("\\n") || value.includes('\\"')) {
    const unescaped = unescapeJsonish(value);
    const nested = formatJson(unescaped);
    if (nested !== undefined) {
      return { format: "json", text: nested };
    }
    return { format, text: unescaped };
  }
  return { format, text: value };
}

function previewForHit(hit: SearchHit): SearchHitDisplay["preview"] {
  const snippet = (hit.snippet ?? "").trim();
  switch (hit.hit_kind.toLowerCase()) {
    case "file":
      return undefined;
    case "code":
    case "symbol":
      return previewFromText(snippet, "code");
    case "tool": {
      const detail = hit.context?.trim() || hit.path?.trim();
      const lines = [
        `${hit.source === "message" ? "Recorded input to" : "Recorded output from"} ${softTitle(hit.title)}.`,
        detail ? softTitle(detail) : undefined,
        hit.handle?.trim() ? `Evidence: ${hit.handle.trim()}` : undefined,
      ];
      return { format: "prose", text: lines.filter(Boolean).join("\n") };
    }
    default:
      return previewFromText(snippet);
  }
}

export function searchHitDisplay(hit: SearchHit): SearchHitDisplay {
  const path = hit.path?.trim() || undefined;
  const line = hit.line && hit.line > 0 ? hit.line : undefined;
  const title = softTitle(hit.title);
  const matches = hit.title_highlights?.length ? highlightIndexes(hit.title, hit.title_highlights) : [];
  const display: SearchHitDisplay = {
    title,
    ...(matches.length ? { titleMatches: matches.filter((index) => index < title.length) } : {}),
    context: hit.context?.trim() || undefined,
    preview: previewForHit(hit),
    kindLabel: kindDisplayLabel(hit),
    openLabel: openLabelFor(hit),
    pivotPath: path,
    openPath: path ? { path, line } : undefined,
    openUrl: hit.url?.trim() || undefined,
  };
  return { ...display, preview: usefulPreview(display) };
}

function normalizeForCompare(text: string): string {
  return text.replace(/\s+/g, " ").trim().toLowerCase();
}

// Repeated titles do not add detail.
function usefulPreview(
  display: SearchHitDisplay,
): SearchHitDisplay["preview"] {
  const preview = display.preview;
  if (!preview) return undefined;
  const body = normalizeForCompare(preview.text);
  const title = normalizeForCompare(display.title);
  if (!body || body === title) return undefined;
  if (
    preview.format === "prose" &&
    body.startsWith(title) &&
    body.length < title.length + 8
  ) {
    return undefined;
  }
  return preview;
}
