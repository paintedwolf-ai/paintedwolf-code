import { DEFAULT_TRANSCRIPT_SPACING, type TranscriptSpacing } from "./transcript-spacing.ts";
import { Lexer, type Token, type Tokens } from "marked";

const tokensBySource = new Map<string, Token[]>();
const CACHE_CHARACTERS = 512 * 1024;
let cachedCharacters = 0;

function tokensFor(source: string): Token[] {
  const previous = tokensBySource.get(source);
  if (previous) return previous;
  const tokens = Lexer.lex(source, { gfm: true });
  if (source.length <= CACHE_CHARACTERS / 8) {
    tokensBySource.set(source, tokens);
    cachedCharacters += source.length;
    while (cachedCharacters > CACHE_CHARACTERS || tokensBySource.size > 128) {
      const oldest = tokensBySource.keys().next().value;
      if (oldest === undefined) break;
      tokensBySource.delete(oldest);
      cachedCharacters -= oldest.length;
    }
  }
  return tokens;
}

export const PROPORTIONAL_GLYPH_EM = 0.5;
export const MONOSPACE_GLYPH_EM = 0.6;
export const WIDE_GLYPH_EM = 1.0;
const INLINE_CODE_PADDING_EM = 0.4;
const WIDE_GLYPH_RE =
  /[\u1100-\u115f\u2e80-\u303e\u3040-\u33ff\u3400-\u4dbf\u4e00-\u9fff\ua960-\ua97f\uac00-\ud7ff\uf900-\ufaff\ufe10-\ufe6f\uff01-\uff60\uffe0-\uffe6]/u;

/** Estimates horizontal advance in `em` for a single line of text, distinguishing wide CJK glyphs. */
export function estimateLineEmWidth(line: string, baseGlyphEm = PROPORTIONAL_GLYPH_EM): number {
  if (!WIDE_GLYPH_RE.test(line)) return line.length * baseGlyphEm;
  let em = 0;
  for (const ch of line) {
    em += WIDE_GLYPH_RE.test(ch) ? WIDE_GLYPH_EM : baseGlyphEm;
  }
  return em;
}

function appendInlineLineEms(tokens: Token[] | undefined, fallback: string, lines: number[]): void {
  if (!tokens || tokens.length === 0) {
    const parts = fallback.split("\n");
    for (let i = 0; i < parts.length; i += 1) {
      if (i > 0) lines.push(0);
      lines[lines.length - 1]! += estimateLineEmWidth(parts[i]!);
    }
    return;
  }
  for (const token of tokens) {
    if (token.type === "br") {
      lines.push(0);
      continue;
    }
    if (token.type === "codespan") {
      const text = "text" in token ? token.text : token.raw;
      lines[lines.length - 1]! +=
        estimateLineEmWidth(text, MONOSPACE_GLYPH_EM) + INLINE_CODE_PADDING_EM;
      continue;
    }
    if ("tokens" in token && token.tokens) {
      appendInlineLineEms(token.tokens, "text" in token ? token.text : token.raw, lines);
      continue;
    }
    const raw = "text" in token ? token.text : token.raw;
    appendInlineLineEms(undefined, raw, lines);
  }
}

function wrappedTokens(
  tokens: Token[] | undefined,
  fallback: string,
  width: number,
  font: number,
  lineHeight = DEFAULT_TRANSCRIPT_SPACING.proseLineHeight,
): number {
  const lineEms = [0];
  appendInlineLineEms(tokens, fallback, lineEms);
  const lineCapacityEm = Math.max(PROPORTIONAL_GLYPH_EM, Math.floor(width / (font * PROPORTIONAL_GLYPH_EM)) * PROPORTIONAL_GLYPH_EM);
  const totalLines = lineEms.reduce(
    (sum, em) => sum + Math.max(1, Math.ceil(em / lineCapacityEm)),
    0,
  );
  return totalLines * font * lineHeight;
}

function blockHeight(token: Token, width: number, font: number, spacing: TranscriptSpacing): number {
  switch (token.type) {
    case "space": case "def": return 0;
    case "code": {
      const code = token as Tokens.Code;
      // Fences scroll horizontally; their source line width does not wrap.
      return code.text.split("\n").length * font * spacing.codeLineHeight + font * (2 * spacing.codePaddingY + spacing.codeGap);
    }
    case "heading": {
      const heading = token as Tokens.Heading;
      const size = font * (heading.depth === 1 ? 1.5 : heading.depth === 2 ? 1.2 : 1);
      return wrappedTokens(heading.tokens, heading.text, width, size, spacing.headingLineHeight) + size * (spacing.headingBefore + spacing.headingAfter);
    }
    case "table": {
      const table = token as Tokens.Table;
      const columns = Math.max(1, table.header.length);
      const cellWidth = Math.max(font, width / columns - font * (2 * spacing.cellPaddingX));
      return [table.header, ...table.rows].reduce((height, row) => height +
        Math.max(...row.map((cell) => wrappedTokens(cell.tokens, cell.text, cellWidth, font, spacing.proseLineHeight))) + font * (2 * spacing.cellPaddingY) + 1, 0) + font * spacing.tableGap;
    }
    case "list": {
      const list = token as Tokens.List;
      return list.items.reduce((height, item) => height + blocksHeight(item.tokens, width - font * spacing.listIndent, font, spacing) + font * (2 * spacing.listItemGap), 0);
    }
    case "blockquote": return blocksHeight((token as Tokens.Blockquote).tokens, width - font, font, spacing);
    case "hr": return font * (2 * spacing.ruleGap) + 1;
    default: return wrappedTokens("tokens" in token ? token.tokens : undefined, "text" in token ? token.text : token.raw, width, font, spacing.proseLineHeight) + font * spacing.paragraphGap;
  }
}

function blocksHeight(tokens: Token[], width: number, font: number, spacing: TranscriptSpacing): number {
  return tokens.reduce((height, token) => height + blockHeight(token, width, font, spacing), 0);
}

/** Estimates rendered blocks, including table padding and unwrapped code. */
export function markdownHeightEstimate(source: string, width: number, font: number, spacing: TranscriptSpacing = DEFAULT_TRANSCRIPT_SPACING): number {
  if (!source.trim() || width <= 0 || font <= 0) return 0;
  return Math.max(font * spacing.proseLineHeight, blocksHeight(tokensFor(source), width, font, spacing) - font * spacing.paragraphGap);
}
