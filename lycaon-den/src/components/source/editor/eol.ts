/** EOL detection and editor normalization; the host restores the file's EOL on save. */

import {
  detectedPlatform,
  type TauriPlatform,
} from "../../../platform/runtime.ts";

export type EolKind = "lf" | "crlf";

export type EolAnalysis = { eol: EolKind; mixed: boolean };

/**
 * Majority vote over newline sequences. Empty / no newlines use the host default.
 */
export function analyzeEol(text: string): EolAnalysis {
  let crlf = 0;
  let lf = 0;
  let cr = 0;
  for (let i = 0; i < text.length; i++) {
    if (text[i] === "\r" && text[i + 1] === "\n") {
      crlf++;
      i++;
      continue;
    }
    if (text[i] === "\n") lf++;
    if (text[i] === "\r") cr++;
  }
  if (crlf === 0 && lf === 0 && cr === 0) {
    return { eol: defaultEol(), mixed: false };
  }
  const kinds = (crlf > 0 ? 1 : 0) + (lf > 0 ? 1 : 0) + (cr > 0 ? 1 : 0);
  // Bare CR normalizes to LF and requires disclosure even without mixed endings.
  return { eol: crlf > lf ? "crlf" : "lf", mixed: kinds > 1 || cr > 0 };
}

/** Use the platform's default line ending. */
export function defaultEol(
  platform: TauriPlatform | null = detectedPlatform(),
): EolKind {
  return platform === "windows" ? "crlf" : "lf";
}

/** Normalize editor text to `\n` and retain the wire EOL. */
export function normalizeEolForEditor(text: string): {
  text: string;
  eol: EolKind;
  mixed: boolean;
} {
  const { eol, mixed } = analyzeEol(text);
  return {
    text: text.replace(/\r\n/g, "\n").replace(/\r/g, "\n"),
    eol,
    mixed,
  };
}
