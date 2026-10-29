import { SCROLL_EPSILON_PX } from "./scrollport-motion-types.ts";
import { scrollportMotionContaining } from "./scrollport-motion.ts";
import { scrollportRevealDelta } from "./scrollport-reveal.ts";

/** Computed properties that decide where a textarea wraps and places lines. */
const MIRRORED_PROPERTIES = [
  "direction",
  "box-sizing",
  "width",
  "border-top-width",
  "border-right-width",
  "border-bottom-width",
  "border-left-width",
  "border-style",
  "padding-top",
  "padding-right",
  "padding-bottom",
  "padding-left",
  "font-style",
  "font-variant",
  "font-weight",
  "font-stretch",
  "font-size",
  "font-size-adjust",
  "font-family",
  "font-feature-settings",
  "font-kerning",
  "font-variation-settings",
  "line-height",
  "text-align",
  "text-transform",
  "text-indent",
  "letter-spacing",
  "word-spacing",
  "word-break",
  "tab-size",
] as const;

export type TextareaCaretLine = { top: number; bottom: number };

function px(value: string): number {
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function caretIndex(textarea: HTMLTextAreaElement): number {
  return textarea.selectionDirection === "backward"
    ? textarea.selectionStart
    : textarea.selectionEnd;
}

/**
 * Measures the caret's line box against the textarea's border box. A hidden mirror lays out
 * the same text with the same wrapping metrics, so soft-wrapped lines count like hard ones.
 */
export function measureTextareaCaretLine(textarea: HTMLTextAreaElement): TextareaCaretLine {
  const doc = textarea.ownerDocument;
  const style = doc.defaultView!.getComputedStyle(textarea);
  const mirror = doc.createElement("div");
  for (const property of MIRRORED_PROPERTIES) {
    mirror.style.setProperty(property, style.getPropertyValue(property));
  }
  Object.assign(mirror.style, {
    position: "absolute",
    top: "0",
    left: "-9999px",
    visibility: "hidden",
    overflow: "hidden",
    height: "auto",
    whiteSpace: "pre-wrap",
    overflowWrap: "break-word",
  });
  const caret = caretIndex(textarea);
  mirror.textContent = textarea.value.slice(0, caret);
  const marker = doc.createElement("span");
  // The rest of the text keeps the caret's word wrapping as it does in the field.
  marker.textContent = textarea.value.slice(caret) || ".";
  mirror.appendChild(marker);
  doc.body.appendChild(mirror);
  try {
    const top = marker.offsetTop + px(style.borderTopWidth);
    const lineHeight = px(style.lineHeight) || px(style.fontSize) * 1.2;
    return { top, bottom: top + lineHeight };
  } finally {
    mirror.remove();
  }
}

/**
 * Keeps a fitted textarea's caret line visible in the scrollport viewport that scrolls it.
 * The field's own vertical padding stays in view around the line, so the draft's first and
 * last lines land against their natural edges.
 */
export function revealTextareaCaret(
  textarea: HTMLTextAreaElement,
  viewport: HTMLElement,
): void {
  if (viewport.scrollHeight - viewport.clientHeight <= SCROLL_EPSILON_PX) return;
  const line = measureTextareaCaretLine(textarea);
  const style = textarea.ownerDocument.defaultView!.getComputedStyle(textarea);
  const fieldTop = textarea.getBoundingClientRect().top - textarea.scrollTop;
  const top = fieldTop + line.top - px(style.paddingTop);
  const bottom = fieldTop + line.bottom + px(style.paddingBottom);
  const motion = scrollportMotionContaining(viewport);
  if (motion) {
    motion.revealSpan(top, bottom);
    return;
  }
  const bounds = viewport.getBoundingClientRect();
  const delta = scrollportRevealDelta(bounds, { top, bottom });
  if (Math.abs(delta) >= SCROLL_EPSILON_PX) viewport.scrollTop += delta;
}
