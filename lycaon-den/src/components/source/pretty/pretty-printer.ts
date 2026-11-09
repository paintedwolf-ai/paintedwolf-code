import {
  countNewlinesSlice,
  type PrettyBudgetTracker,
  utf8ByteLengthSlice,
} from "./pretty-limits.ts";
import type { PrettyOriginRun } from "./pretty-types.ts";

type JsonTokenKind = "{" | "}" | "[" | "]" | ":" | "," | "string" | "number" | "literal";

interface ScannedToken {
  readonly kind: JsonTokenKind;
  readonly from: number;
  readonly to: number;
  readonly utf8Bytes: number;
}

type ContainerState =
  | { kind: "object"; step: "key-or-close" | "key-only" | "colon" | "value" | "after-value" }
  | { kind: "array"; step: "value-or-close" | "value-only" | "after-value" };

export type PrintJsonSegmentResult =
  | {
      readonly status: "formatted";
      readonly displayText: string;
      readonly runs: readonly PrettyOriginRun[];
    }
  | { readonly status: "malformed" }
  | { readonly status: "limit-or-canceled" };

function isJsonWhitespace(code: number): boolean {
  return code === 0x20 || code === 0x09 || code === 0x0a || code === 0x0d;
}

function isTokenBoundary(code: number | undefined): boolean {
  if (code === undefined) return true;
  return (
    isJsonWhitespace(code) ||
    code === 0x2c || // ,
    code === 0x7d || // }
    code === 0x5d || // ]
    code === 0x3a    // :
  );
}

function isHexDigit(code: number): boolean {
  return (
    (code >= 0x30 && code <= 0x39) ||
    (code >= 0x41 && code <= 0x46) ||
    (code >= 0x61 && code <= 0x66)
  );
}

function isAsciiDigit(code: number): boolean {
  return code >= 0x30 && code <= 0x39;
}

function scanStringToken(
  text: string,
  from: number,
  end: number,
  budget: PrettyBudgetTracker,
): ScannedToken | "malformed" | "budget" {
  let i = from + 1;
  while (i < end) {
    if (!budget.recordVisit(1)) return "budget";
    const code = text.charCodeAt(i);
    if (code === 0x22) {
      const to = i + 1;
      return {
        kind: "string",
        from,
        to,
        utf8Bytes: utf8ByteLengthSlice(text, from, to),
      };
    }
    if (code < 0x20) return "malformed";
    if (code === 0x5c) {
      if (i + 1 >= end) return "malformed";
      if (!budget.recordVisit(1)) return "budget";
      const esc = text.charCodeAt(i + 1);
      if (
        esc === 0x22 ||
        esc === 0x5c ||
        esc === 0x2f ||
        esc === 0x62 ||
        esc === 0x66 ||
        esc === 0x6e ||
        esc === 0x72 ||
        esc === 0x74
      ) {
        i += 2;
        continue;
      }
      if (esc === 0x75) {
        if (i + 5 >= end) return "malformed";
        if (!budget.recordVisit(4)) return "budget";
        if (
          !isHexDigit(text.charCodeAt(i + 2)) ||
          !isHexDigit(text.charCodeAt(i + 3)) ||
          !isHexDigit(text.charCodeAt(i + 4)) ||
          !isHexDigit(text.charCodeAt(i + 5))
        ) {
          return "malformed";
        }
        i += 6;
        continue;
      }
      return "malformed";
    }
    i += 1;
  }
  return "malformed";
}

function scanNumberToken(
  text: string,
  from: number,
  end: number,
  budget: PrettyBudgetTracker,
): ScannedToken | "malformed" | "budget" {
  let i = from;
  if (text.charCodeAt(i) === 0x2d) {
    i += 1;
    if (i >= end || !isAsciiDigit(text.charCodeAt(i))) return "malformed";
  }
  if (i >= end || !isAsciiDigit(text.charCodeAt(i))) return "malformed";
  if (text.charCodeAt(i) === 0x30) {
    if (!budget.recordVisit(1)) return "budget";
    i += 1;
    if (i < end && isAsciiDigit(text.charCodeAt(i))) return "malformed";
  } else {
    while (i < end && isAsciiDigit(text.charCodeAt(i))) {
      if (!budget.recordVisit(1)) return "budget";
      i += 1;
    }
  }
  if (i < end && text.charCodeAt(i) === 0x2e) {
    if (!budget.recordVisit(1)) return "budget";
    i += 1;
    if (i >= end || !isAsciiDigit(text.charCodeAt(i))) return "malformed";
    while (i < end && isAsciiDigit(text.charCodeAt(i))) {
      if (!budget.recordVisit(1)) return "budget";
      i += 1;
    }
  }
  if (i < end && (text.charCodeAt(i) === 0x65 || text.charCodeAt(i) === 0x45)) {
    if (!budget.recordVisit(1)) return "budget";
    i += 1;
    if (i < end && (text.charCodeAt(i) === 0x2b || text.charCodeAt(i) === 0x2d)) {
      if (!budget.recordVisit(1)) return "budget";
      i += 1;
    }
    if (i >= end || !isAsciiDigit(text.charCodeAt(i))) return "malformed";
    while (i < end && isAsciiDigit(text.charCodeAt(i))) {
      if (!budget.recordVisit(1)) return "budget";
      i += 1;
    }
  }
  const after = i < end ? text.charCodeAt(i) : undefined;
  if (!isTokenBoundary(after)) return "malformed";
  return {
    kind: "number",
    from,
    to: i,
    utf8Bytes: i - from,
  };
}

function scanNextToken(
  text: string,
  from: number,
  end: number,
  budget: PrettyBudgetTracker,
): ScannedToken | "malformed" | "budget" {
  if (!budget.recordVisit(1)) return "budget";
  const code = text.charCodeAt(from);
  if (code === 0x7b) return { kind: "{", from, to: from + 1, utf8Bytes: 1 };
  if (code === 0x7d) return { kind: "}", from, to: from + 1, utf8Bytes: 1 };
  if (code === 0x5b) return { kind: "[", from, to: from + 1, utf8Bytes: 1 };
  if (code === 0x5d) return { kind: "]", from, to: from + 1, utf8Bytes: 1 };
  if (code === 0x3a) return { kind: ":", from, to: from + 1, utf8Bytes: 1 };
  if (code === 0x2c) return { kind: ",", from, to: from + 1, utf8Bytes: 1 };
  if (code === 0x22) return scanStringToken(text, from, end, budget);
  if (code === 0x2d || isAsciiDigit(code)) {
    return scanNumberToken(text, from, end, budget);
  }
  if (code === 0x74 && from + 4 <= end && text.startsWith("true", from)) {
    if (!budget.recordVisit(3)) return "budget";
    if (!isTokenBoundary(from + 4 < end ? text.charCodeAt(from + 4) : undefined)) {
      return "malformed";
    }
    return { kind: "literal", from, to: from + 4, utf8Bytes: 4 };
  }
  if (code === 0x66 && from + 5 <= end && text.startsWith("false", from)) {
    if (!budget.recordVisit(4)) return "budget";
    if (!isTokenBoundary(from + 5 < end ? text.charCodeAt(from + 5) : undefined)) {
      return "malformed";
    }
    return { kind: "literal", from, to: from + 5, utf8Bytes: 5 };
  }
  if (code === 0x6e && from + 4 <= end && text.startsWith("null", from)) {
    if (!budget.recordVisit(3)) return "budget";
    if (!isTokenBoundary(from + 4 < end ? text.charCodeAt(from + 4) : undefined)) {
      return "malformed";
    }
    return { kind: "literal", from, to: from + 4, utf8Bytes: 4 };
  }
  return "malformed";
}

function isValueToken(kind: JsonTokenKind): boolean {
  return (
    kind === "{" ||
    kind === "[" ||
    kind === "string" ||
    kind === "number" ||
    kind === "literal"
  );
}

/**
 * Tokenizes and pretty-prints a strict JSON object/array slice [srcFrom, srcTo).
 * Preserves exact token lexemes, duplicate keys, key order, and string escapes.
 */
export function printJsonSegment(
  rawText: string,
  srcFrom: number,
  srcTo: number,
  dispOffset: number,
  segmentIndex: number,
  indentUnit: string,
  budget: PrettyBudgetTracker,
): PrintJsonSegmentResult {
  const savedUtf16 = budget.counters.outputUtf16Units;
  const savedUtf8 = budget.counters.outputUtf8Bytes;
  const savedLines = budget.counters.outputLines;
  const savedRuns = budget.counters.mappingRuns;
  const savedTokens = budget.counters.tokens;
  const rollbackOutput = (): "malformed" => {
    budget.counters.outputUtf16Units = savedUtf16;
    budget.counters.outputUtf8Bytes = savedUtf8;
    budget.counters.outputLines = savedLines;
    budget.counters.mappingRuns = savedRuns;
    budget.counters.tokens = savedTokens;
    return "malformed";
  };

  if (srcFrom >= srcTo) return { status: rollbackOutput() };
  const firstCode = rawText.charCodeAt(srcFrom);
  if (firstCode !== 0x7b && firstCode !== 0x5b) {
    return { status: rollbackOutput() };
  }

  const runs: PrettyOriginRun[] = [];
  const chunks: string[] = [];
  let currentDisp = dispOffset;
  const indentUtf8 = utf8ByteLengthSlice(indentUnit);

  const emitVirtual = (
    layoutText: string,
    layoutUtf8: number,
    newlines: number,
    wsFrom: number,
    wsTo: number,
    insertOffset: number,
  ): boolean => {
    if (layoutText.length === 0 && wsFrom === wsTo) return true;
    if (!budget.recordOutputRun(layoutText.length, layoutUtf8, newlines)) {
      return false;
    }
    runs.push({
      kind: "virtual-layout",
      dispFrom: currentDisp,
      dispTo: currentDisp + layoutText.length,
      srcFrom: wsFrom,
      srcTo: wsTo,
      insertOffset,
      segmentIndex,
    });
    if (layoutText.length > 0) chunks.push(layoutText);
    currentDisp += layoutText.length;
    return true;
  };

  const emitIdentity = (token: ScannedToken): boolean => {
    const len = token.to - token.from;
    const newlines = countNewlinesSlice(rawText, token.from, token.to);
    if (!budget.recordToken()) return false;
    if (!budget.recordOutputRun(len, token.utf8Bytes, newlines)) return false;
    runs.push({
      kind: "identity",
      dispFrom: currentDisp,
      dispTo: currentDisp + len,
      srcFrom: token.from,
      srcTo: token.to,
      insertOffset: token.from,
      segmentIndex,
    });
    chunks.push(rawText.slice(token.from, token.to));
    currentDisp += len;
    return true;
  };

  const firstToken = scanNextToken(rawText, srcFrom, srcTo, budget);
  if (firstToken === "budget") return { status: "limit-or-canceled" };
  if (firstToken === "malformed") return { status: rollbackOutput() };

  const stack: ContainerState[] = [
    firstToken.kind === "{"
      ? { kind: "object", step: "key-or-close" }
      : { kind: "array", step: "value-or-close" },
  ];
  if (!budget.recordDepth(1)) return { status: "limit-or-canceled" };
  if (!emitIdentity(firstToken)) return { status: "limit-or-canceled" };

  let cursor = firstToken.to;

  while (stack.length > 0) {
    const wsFrom = cursor;
    while (cursor < srcTo && isJsonWhitespace(rawText.charCodeAt(cursor))) {
      if (!budget.recordVisit(1)) return { status: "limit-or-canceled" };
      cursor += 1;
    }
    const wsTo = cursor;
    if (wsTo >= srcTo) return { status: rollbackOutput() };

    const token = scanNextToken(rawText, wsTo, srcTo, budget);
    if (token === "budget") return { status: "limit-or-canceled" };
    if (token === "malformed") return { status: rollbackOutput() };

    const top = stack[stack.length - 1]!;
    let layoutText = "";
    let layoutUtf8 = 0;
    let layoutNewlines = 0;
    let insertOffset = wsTo;

    if (top.kind === "object") {
      if (top.step === "key-or-close") {
        if (token.kind === "}") {
          stack.pop();
          insertOffset = wsFrom;
        } else if (token.kind === "string") {
          top.step = "colon";
          const depth = stack.length;
          layoutText = `\n${indentUnit.repeat(depth)}`;
          layoutUtf8 = 1 + depth * indentUtf8;
          layoutNewlines = 1;
          insertOffset = wsTo;
        } else {
          return { status: rollbackOutput() };
        }
      } else if (top.step === "key-only") {
        if (token.kind !== "string") return { status: rollbackOutput() };
        top.step = "colon";
        const depth = stack.length;
        layoutText = `\n${indentUnit.repeat(depth)}`;
        layoutUtf8 = 1 + depth * indentUtf8;
        layoutNewlines = 1;
        insertOffset = wsTo;
      } else if (top.step === "colon") {
        if (token.kind !== ":") return { status: rollbackOutput() };
        top.step = "value";
        insertOffset = wsFrom;
      } else if (top.step === "value") {
        if (!isValueToken(token.kind)) return { status: rollbackOutput() };
        top.step = "after-value";
        layoutText = " ";
        layoutUtf8 = 1;
        insertOffset = wsTo;
        if (token.kind === "{" || token.kind === "[") {
          if (!budget.recordDepth(stack.length + 1)) {
            return { status: "limit-or-canceled" };
          }
          stack.push(
            token.kind === "{"
              ? { kind: "object", step: "key-or-close" }
              : { kind: "array", step: "value-or-close" },
          );
        }
      } else {
        if (token.kind === ",") {
          top.step = "key-only";
          insertOffset = wsFrom;
        } else if (token.kind === "}") {
          stack.pop();
          const depth = stack.length;
          layoutText = `\n${indentUnit.repeat(depth)}`;
          layoutUtf8 = 1 + depth * indentUtf8;
          layoutNewlines = 1;
          insertOffset = wsFrom;
        } else {
          return { status: rollbackOutput() };
        }
      }
    } else {
      if (top.step === "value-or-close") {
        if (token.kind === "]") {
          stack.pop();
          insertOffset = wsFrom;
        } else if (isValueToken(token.kind)) {
          top.step = "after-value";
          const depth = stack.length;
          layoutText = `\n${indentUnit.repeat(depth)}`;
          layoutUtf8 = 1 + depth * indentUtf8;
          layoutNewlines = 1;
          insertOffset = wsTo;
          if (token.kind === "{" || token.kind === "[") {
            if (!budget.recordDepth(stack.length + 1)) {
              return { status: "limit-or-canceled" };
            }
            stack.push(
              token.kind === "{"
                ? { kind: "object", step: "key-or-close" }
                : { kind: "array", step: "value-or-close" },
            );
          }
        } else {
          return { status: rollbackOutput() };
        }
      } else if (top.step === "value-only") {
        if (!isValueToken(token.kind)) return { status: rollbackOutput() };
        top.step = "after-value";
        const depth = stack.length;
        layoutText = `\n${indentUnit.repeat(depth)}`;
        layoutUtf8 = 1 + depth * indentUtf8;
        layoutNewlines = 1;
        insertOffset = wsTo;
        if (token.kind === "{" || token.kind === "[") {
          if (!budget.recordDepth(stack.length + 1)) {
            return { status: "limit-or-canceled" };
          }
          stack.push(
            token.kind === "{"
              ? { kind: "object", step: "key-or-close" }
              : { kind: "array", step: "value-or-close" },
          );
        }
      } else {
        if (token.kind === ",") {
          top.step = "value-only";
          insertOffset = wsFrom;
        } else if (token.kind === "]") {
          stack.pop();
          const depth = stack.length;
          layoutText = `\n${indentUnit.repeat(depth)}`;
          layoutUtf8 = 1 + depth * indentUtf8;
          layoutNewlines = 1;
          insertOffset = wsFrom;
        } else {
          return { status: rollbackOutput() };
        }
      }
    }

    if (
      !emitVirtual(
        layoutText,
        layoutUtf8,
        layoutNewlines,
        wsFrom,
        wsTo,
        insertOffset,
      )
    ) {
      return { status: "limit-or-canceled" };
    }
    if (!emitIdentity(token)) {
      return { status: "limit-or-canceled" };
    }
    cursor = token.to;
  }

  if (cursor !== srcTo) {
    return { status: rollbackOutput() };
  }

  return {
    status: "formatted",
    displayText: chunks.join(""),
    runs,
  };
}
