/**
 * Pure scope resolution for inline edit (selection → enclosing symbol → ±20 lines).
 */

export type OutlineSymbol = {
  name: string;
  /** 1-based start line. */
  line: number;
};

export type ResolvedInlineEditScope = {
  startLine: number;
  endLine: number;
  kind: "selection" | "symbol" | "window";
  /** Set when kind is symbol. */
  symbolName?: string;
};

export type ResolveInlineEditScopeArgs = {
  /** 1-based inclusive selection; empty ⇒ no selection scope. */
  selection: { startLine: number; endLine: number; empty: boolean };
  /** Caret line (1-based) when selection is empty. */
  caretLine: number;
  docLines: number;
  symbols: readonly OutlineSymbol[];
  /** Half-window when falling back (default 20 ⇒ ±20). */
  windowRadius?: number;
};

/** Build closed ranges from outline starts (end = next start − 1, or doc end). */
export function symbolSpans(
  symbols: readonly OutlineSymbol[],
  docLines: number,
): Array<{ name: string; startLine: number; endLine: number }> {
  const doc = Math.max(1, Math.floor(docLines));
  const sorted = [...symbols]
    .filter((s) => s.line >= 1 && s.line <= doc && s.name.trim())
    .sort((a, b) => a.line - b.line || a.name.localeCompare(b.name));
  const out: Array<{ name: string; startLine: number; endLine: number }> = [];
  for (let i = 0; i < sorted.length; i++) {
    const cur = sorted[i]!;
    const next = sorted[i + 1];
    const end = next ? Math.max(cur.line, next.line - 1) : doc;
    out.push({ name: cur.name, startLine: cur.line, endLine: end });
  }
  return out;
}

/** Innermost span containing caret, or null. */
export function enclosingSymbolAt(
  symbols: readonly OutlineSymbol[],
  caretLine: number,
  docLines: number,
): { name: string; startLine: number; endLine: number } | null {
  const caret = Math.min(Math.max(1, Math.floor(caretLine)), Math.max(1, docLines));
  const spans = symbolSpans(symbols, docLines);
  let best: { name: string; startLine: number; endLine: number } | null = null;
  for (const span of spans) {
    if (caret < span.startLine || caret > span.endLine) continue;
    if (
      !best ||
      span.endLine - span.startLine < best.endLine - best.startLine ||
      (span.endLine - span.startLine === best.endLine - best.startLine &&
        span.startLine > best.startLine)
    ) {
      best = span;
    }
  }
  return best;
}

function clampRange(
  start: number,
  end: number,
  docLines: number,
): { startLine: number; endLine: number } {
  const doc = Math.max(1, Math.floor(docLines));
  let a = Math.min(Math.max(1, Math.floor(start)), doc);
  let b = Math.min(Math.max(1, Math.floor(end)), doc);
  if (b < a) [a, b] = [b, a];
  return { startLine: a, endLine: b };
}

/**
 * Resolve the edit scope. Non-empty selection wins; else innermost enclosing
 * outline symbol; else ±windowRadius lines around the caret.
 */
export function resolveInlineEditScope(
  args: ResolveInlineEditScopeArgs,
): ResolvedInlineEditScope {
  const doc = Math.max(1, Math.floor(args.docLines));
  const radius = Math.max(0, Math.floor(args.windowRadius ?? 20));

  if (!args.selection.empty) {
    const r = clampRange(
      args.selection.startLine,
      args.selection.endLine,
      doc,
    );
    return { ...r, kind: "selection" };
  }

  const enclosing = enclosingSymbolAt(args.symbols, args.caretLine, doc);
  if (enclosing) {
    return {
      startLine: enclosing.startLine,
      endLine: enclosing.endLine,
      kind: "symbol",
      symbolName: enclosing.name,
    };
  }

  const caret = Math.min(Math.max(1, Math.floor(args.caretLine)), doc);
  const r = clampRange(caret - radius, caret + radius, doc);
  return { ...r, kind: "window" };
}
