import type {
  ProjectSourceReadResponse,
  SourceEncoding,
} from "../../api/types.ts";

export const FILE_BUFFER_KINDS = ["text", "image", "info", "diff", "diffs", "walk", "trust", "chat"] as const;
export type FileBufferKind = typeof FILE_BUFFER_KINDS[number];

/** A pane the stage composes itself, with no file document behind it. */
export function isComposedBufferKind(kind: FileBufferKind): kind is "diff" | "diffs" | "walk" | "trust" | "chat" {
  return kind === "diff" || kind === "diffs" || kind === "walk" || kind === "trust" || kind === "chat";
}

/** Image MIME values accepted from host sniffing. */
const LOCKED_IMAGE_MIMES = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/svg+xml",
  "image/bmp",
  "image/x-icon",
  "image/vnd.microsoft.icon",
  "image/avif",
]);

export function isLockedImageMime(mime: string | null | undefined): boolean {
  const m = (mime ?? "").trim().toLowerCase();
  return m.length > 0 && LOCKED_IMAGE_MIMES.has(m);
}

export function bufferKindFromSourceRead(
  res: Pick<
    ProjectSourceReadResponse,
    "content" | "binary" | "over_limit" | "mime"
  >,
): FileBufferKind {
  if (!res.binary && !res.over_limit) return "text";
  if (res.binary && isLockedImageMime(res.mime)) return "image";
  return "info";
}

type InfoCardVariant = "over_limit" | "binary" | "unsupported_encoding";

export function infoCardVariant(args: {
  overLimit: boolean;
  unsupportedEncodingDetected?: string | null;
}): InfoCardVariant {
  if ((args.unsupportedEncodingDetected ?? "").trim()) {
    return "unsupported_encoding";
  }
  return args.overLimit ? "over_limit" : "binary";
}

export function infoCardTitle(variant: InfoCardVariant): string {
  if (variant === "unsupported_encoding") {
    return "Can't open this file safely";
  }
  return "";
}

export function infoCardExplanation(
  variant: InfoCardVariant,
  detected?: string | null,
): string {
  if (variant === "over_limit") {
    return "This file is larger than the in-app editor opens.";
  }
  if (variant === "unsupported_encoding") {
    const label = (detected ?? "").trim();
    if (!label || label === "unknown") {
      return "This file uses an unsupported or malformed text encoding, so opening it here could corrupt it. Convert it to UTF-8 or UTF-16 in an external editor, then reopen it.";
    }
    return `This file uses ${label}, which this editor can't round-trip safely. Convert it to UTF-8 or UTF-16 in an external editor, then reopen it.`;
  }
  return "This is a binary file. If you know it is BOM-less UTF-16 text, choose its byte order below.";
}

const ENCODING_CHIP_LABELS: Record<SourceEncoding, string | null> = {
  "utf-8": null,
  "utf-8-bom": "UTF-8 BOM",
  "utf-16le": "UTF-16 LE",
  "utf-16le-bom": "UTF-16 LE BOM",
  "utf-16be": "UTF-16 BE",
  "utf-16be-bom": "UTF-16 BE BOM",
};

export function encodingChipLabel(
  encoding: SourceEncoding | null | undefined,
): string | null {
  return encoding ? ENCODING_CHIP_LABELS[encoding] : null;
}
