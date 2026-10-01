/** Paints redaction spans after markdown rewrites source offsets. */
import type { RedactedSpan } from "../../api/types.ts";
import {
  REDACTION_MARK_CLASS,
  REDACTION_MASK_CLASS,
  REDACTION_REFERENCE_CLASS,
  redactionMarkModifier,
  redactionMarkTitle,
} from "../transcript/content/redaction-spans.ts";

const REDACTION_DATA_PREFIX = "data-redaction-";

// Private-use delimiters survive markdown rendering unchanged.
const SENTINEL_OPEN = "\uE000";
const SENTINEL_CLOSE = "\uE001";

export type ProseRedactionMark = { span: RedactedSpan; text: string };

export type ProseRedactionPlan = {
  source: string;
  marks: Map<string, ProseRedactionMark>;
};

function sentinel(nonce: string, index: number): string {
  return `${SENTINEL_OPEN}${nonce}${index.toString(16).padStart(4, "0")}${SENTINEL_CLOSE}`;
}

function renderNonce(): string {
  return crypto.randomUUID().replace(/-/gu, "");
}

/** Replaces each span with a render-specific sentinel. */
export function planProseRedaction(
  content: string,
  spans: readonly RedactedSpan[],
): ProseRedactionPlan | null {
  if (spans.length === 0) return null;
  const points = Array.from(content);
  const marks = new Map<string, ProseRedactionMark>();
  const nonce = renderNonce();
  let out = "";
  let cursor = 0;
  for (const span of spans) {
    const start = Math.max(cursor, Math.min(span.start, points.length));
    const end = Math.max(start, Math.min(start + span.length, points.length));
    if (end === start) continue;
    const token = sentinel(nonce, marks.size);
    marks.set(token, { span, text: points.slice(start, end).join("") });
    out += points.slice(cursor, start).join("") + token;
    cursor = end;
  }
  if (marks.size === 0) return null;
  out += points.slice(cursor).join("");
  return { source: out, marks };
}

/** Removes redaction metadata not stamped by this render. */
function neutralizeForgedMarks(root: HTMLElement): void {
  for (const el of root.querySelectorAll("*")) {
    el.classList.remove(REDACTION_MARK_CLASS, REDACTION_MASK_CLASS, REDACTION_REFERENCE_CLASS);
    if (el.classList.length === 0) el.removeAttribute("class");
    for (const name of [...el.getAttributeNames()]) {
      if (name.startsWith(REDACTION_DATA_PREFIX)) el.removeAttribute(name);
    }
  }
}

function markElement(doc: Document, mark: ProseRedactionMark): HTMLElement {
  const el = doc.createElement("span");
  el.className = REDACTION_MARK_CLASS;
  const modifier = redactionMarkModifier(mark.span);
  if (modifier) el.classList.add(modifier);
  el.setAttribute("data-tip", redactionMarkTitle(mark.span));
  el.setAttribute("data-redaction-kind", mark.span.kind);
  if (mark.span.rule_id) el.setAttribute("data-redaction-rule", mark.span.rule_id);
  el.textContent = mark.text;
  return el;
}

const SENTINEL_RE = new RegExp(
  `${SENTINEL_OPEN}[0-9a-f]{36}${SENTINEL_CLOSE}`,
  "gu",
);

function swapSentinels(root: HTMLElement, plan: ProseRedactionPlan): void {
  const doc = root.ownerDocument;
  const walker = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const targets: Text[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeValue?.includes(SENTINEL_OPEN)) targets.push(node as Text);
  }
  for (const node of targets) {
    const text = node.nodeValue ?? "";
    const fragment = doc.createDocumentFragment();
    let last = 0;
    for (const match of text.matchAll(SENTINEL_RE)) {
      const mark = plan.marks.get(match[0]);
      // Preserve sentinels from another render as text.
      if (!mark) continue;
      const at = match.index;
      if (at > last) fragment.append(text.slice(last, at));
      fragment.append(markElement(doc, mark));
      last = at + match[0].length;
    }
    if (last === 0) continue;
    if (last < text.length) fragment.append(text.slice(last));
    node.replaceWith(fragment);
  }
}

/** Removes unstamped metadata and paints this render's marks. */
export function paintProseRedaction(
  html: string,
  plan: ProseRedactionPlan | null,
): string {
  const forgeable =
    html.includes(REDACTION_MARK_CLASS) || html.includes(REDACTION_DATA_PREFIX);
  if (!plan && !forgeable) return html;
  const host = document.createElement("div");
  host.innerHTML = html;
  if (forgeable) neutralizeForgedMarks(host);
  if (plan) swapSentinels(host, plan);
  return host.innerHTML;
}
