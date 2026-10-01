import type { AttachmentCapabilities } from "../../api/types.ts";

export const INLINE_TEXT_TOO_LARGE_MESSAGE =
  "Message text is too large. Attach the text as a file instead.";

export type InlineTextCapabilities = Pick<
  AttachmentCapabilities,
  "auto_attach_paste_bytes" | "max_inline_text_bytes"
>;

/** Counts UTF-8 bytes without allocating an encoded copy. */
export function utf8ByteLength(text: string): number {
  let bytes = 0;
  for (let index = 0; index < text.length; index += 1) {
    const codeUnit = text.charCodeAt(index);
    if (codeUnit <= 0x7f) {
      bytes += 1;
    } else if (codeUnit <= 0x7ff) {
      bytes += 2;
    } else if (
      codeUnit >= 0xd800 &&
      codeUnit <= 0xdbff &&
      index + 1 < text.length &&
      text.charCodeAt(index + 1) >= 0xdc00 &&
      text.charCodeAt(index + 1) <= 0xdfff
    ) {
      bytes += 4;
      index += 1;
    } else {
      // Unpaired surrogates encode as U+FFFD.
      bytes += 3;
    }
  }
  return bytes;
}

/** Evaluates paste bytes before textarea mutation. */
export function shouldAttachTextPaste(args: {
  current: string;
  selectionStart: number;
  selectionEnd: number;
  pasted: string;
  capabilities: InlineTextCapabilities | undefined;
}): boolean {
  const pastedBytes = utf8ByteLength(args.pasted);
  const start = Math.max(0, Math.min(args.selectionStart, args.current.length));
  const end = Math.max(start, Math.min(args.selectionEnd, args.current.length));
  const nextDraftBytes =
    utf8ByteLength(args.current.slice(0, start)) +
    pastedBytes +
    utf8ByteLength(args.current.slice(end));
  const capabilities = args.capabilities;
  if (
    capabilities &&
    (pastedBytes >= capabilities.auto_attach_paste_bytes ||
      nextDraftBytes > capabilities.max_inline_text_bytes)
  ) {
    return true;
  }
  return false;
}

export function inlineTextWithinLimit(
  text: string,
  capabilities: InlineTextCapabilities | undefined,
): boolean {
  return !capabilities || utf8ByteLength(text) <= capabilities.max_inline_text_bytes;
}
