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

function inlineText(tokens: Token[] | undefined, fallback: string): string {
  if (!tokens) return fallback;
  return tokens.map((token) => {
    if (token.type === "br") return "\n";
    if ("tokens" in token) return inlineText(token.tokens, "text" in token ? token.text : token.raw);
    return "text" in token ? token.text : token.raw;
  }).join("");
}

function wrapped(text: string, width: number, font: number, lineHeight = DEFAULT_TRANSCRIPT_SPACING.proseLineHeight): number {
  const capacity = Math.max(1, Math.floor(width / (font * 0.5)));
  return text.split("\n").reduce((sum, line) => sum + Math.max(1, Math.ceil(line.length / capacity)), 0) * font * lineHeight;
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
      return wrapped(inlineText(heading.tokens, heading.text), width, size, spacing.headingLineHeight) + size * (spacing.headingBefore + spacing.headingAfter);
    }
    case "table": {
      const table = token as Tokens.Table;
      const columns = Math.max(1, table.header.length);
      const cellWidth = Math.max(font, width / columns - font * (2 * spacing.cellPaddingX));
      return [table.header, ...table.rows].reduce((height, row) => height +
        Math.max(...row.map((cell) => wrapped(inlineText(cell.tokens, cell.text), cellWidth, font, spacing.proseLineHeight))) + font * (2 * spacing.cellPaddingY) + 1, 0) + font * spacing.tableGap;
    }
    case "list": {
      const list = token as Tokens.List;
      return list.items.reduce((height, item) => height + blocksHeight(item.tokens, width - font * spacing.listIndent, font, spacing) + font * (2 * spacing.listItemGap), 0);
    }
    case "blockquote": return blocksHeight((token as Tokens.Blockquote).tokens, width - font, font, spacing);
    case "hr": return font * (2 * spacing.ruleGap) + 1;
    default: return wrapped(inlineText("tokens" in token ? token.tokens : undefined, "text" in token ? token.text : token.raw), width, font, spacing.proseLineHeight) + font * spacing.paragraphGap;
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
