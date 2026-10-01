import { Lexer, type Token } from "marked";
import { prepareMarkdownSource } from "./markdown-output.ts";

export const MARKDOWN_PREVIEW_ASYNC_CHARS = 32_768;
const BLOCK_TARGET_CHARS = 4_096;
const BLOCK_MAX_TOKENS = 24;

export type MarkdownPreviewBlock = { chars: number; lines: number };
export type MarkdownPreviewRequest =
  | { type: "prepare"; source: string }
  | { type: "block"; index: number };
export type MarkdownPreviewResponse =
  | { type: "ready"; blocks: MarkdownPreviewBlock[] }
  | { type: "block"; index: number; tokens: Token[] }
  | { type: "error" };

/** Lex once so references and nested Markdown retain whole-document context. */
export function prepareMarkdownPreview(source: string): Token[][] {
  const tokens = Lexer.lex(prepareMarkdownSource(source), { gfm: true });
  const blocks: Token[][] = [];
  let block: Token[] = [];
  let chars = 0;
  for (const token of tokens) {
    if (token.type === "space" || token.type === "def") continue;
    if (block.length && (chars + token.raw.length > BLOCK_TARGET_CHARS || block.length >= BLOCK_MAX_TOKENS)) {
      blocks.push(block);
      block = [];
      chars = 0;
    }
    block.push(token);
    chars += token.raw.length;
  }
  if (block.length) blocks.push(block);
  return blocks;
}

export function describeMarkdownBlock(tokens: Token[]): MarkdownPreviewBlock {
  let chars = 0;
  let lines = 0;
  for (const token of tokens) {
    chars += token.raw.length;
    lines += token.raw.split("\n").length;
  }
  return { chars, lines };
}
