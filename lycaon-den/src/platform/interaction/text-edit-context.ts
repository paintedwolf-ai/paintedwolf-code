/**
 * Cut / Copy / Paste / Select all against editables and live selections.
 */

import { selectAll as cmSelectAll } from "@codemirror/commands";
import { EditorSelection } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import {
  copyTextToClipboard,
  readClipboardText,
} from "../../utils/clipboard.ts";
import { tauriPlatform } from "../runtime.ts";

const NON_TEXT_INPUT_TYPES = new Set([
  "button",
  "checkbox",
  "radio",
  "submit",
  "reset",
  "file",
  "image",
  "range",
  "color",
  "hidden",
]);

export type TextEditableElement =
  | HTMLInputElement
  | HTMLTextAreaElement
  | HTMLElement;

export type EditableSnapshot = {
  el: TextEditableElement;
  /** Character offsets for input / textarea and editor documents. */
  start: number;
  end: number;
  /** Cloned range for contenteditable (may be outside the element). */
  range: Range | null;
  readOnly: boolean;
};

export type TextEditContext =
  | { kind: "editable"; snapshot: EditableSnapshot }
  | { kind: "selection"; text: string; target: EventTarget | null };

function isTextualInput(el: Element): el is HTMLInputElement {
  if (!(el instanceof HTMLInputElement)) return false;
  const type = (el.type || "text").toLowerCase();
  return !NON_TEXT_INPUT_TYPES.has(type);
}

/** Closest textual editable under the event target, or null. */
export function findTextEditable(target: EventTarget | null): TextEditableElement | null {
  if (!(target instanceof Element)) return null;
  const el = target.closest("input, textarea, [contenteditable]");
  if (!el) return null;
  if (el instanceof HTMLInputElement) {
    if (!isTextualInput(el) || el.disabled) return null;
    return el;
  }
  if (el instanceof HTMLTextAreaElement) {
    if (el.disabled) return null;
    return el;
  }
  if (el.getAttribute("contenteditable") === "false") return null;
  return el as HTMLElement;
}

function isReadOnly(el: TextEditableElement): boolean {
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    return el.readOnly;
  }
  return el.getAttribute("contenteditable") === "false";
}

export function snapshotEditable(el: TextEditableElement): EditableSnapshot {
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    const start = el.selectionStart ?? 0;
    const end = el.selectionEnd ?? 0;
    return { el, start, end, range: null, readOnly: isReadOnly(el) };
  }
  // Editor state survives context-menu focus and synchronous select-all.
  if (el instanceof HTMLElement) {
    const view = EditorView.findFromDOM(el);
    if (view) {
      const main = view.state.selection.main;
      return {
        el,
        start: main.from,
        end: main.to,
        range: null,
        readOnly: isReadOnly(el) || view.state.readOnly,
      };
    }
  }
  const sel = typeof window !== "undefined" ? window.getSelection() : null;
  let range: Range | null = null;
  if (sel && sel.rangeCount > 0) {
    const live = sel.getRangeAt(0);
    if (el.contains(live.commonAncestorContainer)) {
      range = live.cloneRange();
    }
  }
  return {
    el,
    start: 0,
    end: 0,
    range,
    readOnly: isReadOnly(el),
  };
}

const SELECTABLE_USER_SELECT = new Set(["text", "all", "contain"]);

function computedUserSelect(el: Element): string {
  try {
    const style = window.getComputedStyle(el);
    return (style.userSelect || "").toLowerCase();
  } catch {
    return "";
  }
}

function startingElement(target: EventTarget | null): Element | null {
  if (target instanceof Element) return target;
  if (target instanceof Node) return target.parentElement;
  return null;
}

function rangeLivesIn(el: Element, range: Range): boolean {
  try {
    if (el.contains(range.commonAncestorContainer)) return true;
    return range.intersectsNode(el);
  } catch {
    return false;
  }
}

/** Selection text when the target is in the same selectable island as the live range. */
export function selectionCopyTextAtTarget(target: EventTarget | null): string | null {
  if (typeof window === "undefined") return null;
  if (!(target instanceof Node)) return null;
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const text = sel.toString();
  if (text.length === 0) return null;
  const range = sel.getRangeAt(0);

  let el: Element | null = startingElement(target);
  let sawNone = false;
  while (el && el !== document.body && el !== document.documentElement) {
    const mode = computedUserSelect(el);
    if (SELECTABLE_USER_SELECT.has(mode)) {
      return rangeLivesIn(el, range) ? text : null;
    }
    if (mode === "none") sawNone = true;
    el = el.parentElement;
  }

  if (sawNone) return null;
  try {
    if (!range.intersectsNode(target)) return null;
  } catch {
    return null;
  }
  return text;
}

/** Editable under the cursor, else a live selection in that selectable island. */
export function resolveTextEditContext(
  target: EventTarget | null,
): TextEditContext | null {
  const editable = findTextEditable(target);
  if (editable) {
    return { kind: "editable", snapshot: snapshotEditable(editable) };
  }
  const text = selectionCopyTextAtTarget(target);
  if (text !== null) {
    return { kind: "selection", text, target };
  }
  return null;
}

export function editableHasSelection(snap: EditableSnapshot): boolean {
  if (snap.el instanceof HTMLInputElement || snap.el instanceof HTMLTextAreaElement) {
    return snap.end > snap.start;
  }
  if (snap.end > snap.start) return true;
  if (!snap.range) return false;
  return !snap.range.collapsed && snap.range.toString().length > 0;
}

export function selectedTextFromSnapshot(snap: EditableSnapshot): string {
  if (snap.el instanceof HTMLInputElement || snap.el instanceof HTMLTextAreaElement) {
    return snap.el.value.slice(snap.start, snap.end);
  }
  if (snap.end > snap.start && snap.el instanceof HTMLElement) {
    const view = EditorView.findFromDOM(snap.el);
    if (view) {
      return view.state.doc.sliceString(snap.start, snap.end);
    }
  }
  return snap.range?.toString() ?? "";
}

function dispatchInput(el: HTMLElement, data: string | null, inputType: string): void {
  el.dispatchEvent(
    new InputEvent("input", {
      bubbles: true,
      cancelable: false,
      data,
      inputType,
    }),
  );
}

function dispatchBeforeInput(el: HTMLElement, data: string | null, inputType: string): boolean {
  return el.dispatchEvent(
    new InputEvent("beforeinput", {
      bubbles: true,
      cancelable: true,
      data,
      inputType,
    }),
  );
}

function replaceInputSelection(
  el: HTMLInputElement | HTMLTextAreaElement,
  start: number,
  end: number,
  insert: string,
  inputType: string,
): void {
  const data = insert.length > 0 ? insert : null;
  if (!dispatchBeforeInput(el, data, inputType)) return;
  const next = el.value.slice(0, start) + insert + el.value.slice(end);
  const caret = start + insert.length;
  el.focus();
  el.value = next;
  try {
    el.setSelectionRange(caret, caret);
  } catch {
    // Some input types reject setSelectionRange.
  }
  dispatchInput(el, data, inputType);
}

function replaceContentEditableSelection(
  el: HTMLElement,
  range: Range | null,
  insert: string,
): void {
  el.focus();
  const sel = window.getSelection();
  if (!sel) return;
  sel.removeAllRanges();
  if (range) {
    sel.addRange(range);
  } else {
    const all = document.createRange();
    all.selectNodeContents(el);
    all.collapse(false);
    sel.addRange(all);
  }
  if (insert.length === 0) {
    if (typeof document.execCommand === "function") {
      document.execCommand("delete");
    } else if (sel.rangeCount > 0) {
      sel.getRangeAt(0).deleteContents();
    }
    dispatchInput(el, null, "deleteContent");
    return;
  }
  if (typeof document.execCommand === "function" && document.execCommand("insertText", false, insert)) {
    dispatchInput(el, insert, "insertText");
    return;
  }
  if (sel.rangeCount > 0) {
    const r = sel.getRangeAt(0);
    r.deleteContents();
    r.insertNode(document.createTextNode(insert));
    r.collapse(false);
  }
  dispatchInput(el, insert, "insertText");
}

function replaceCmSelection(
  view: EditorView,
  from: number,
  to: number,
  insert: string,
): void {
  view.dispatch({
    changes: { from, to, insert },
    selection: EditorSelection.cursor(from + insert.length),
  });
  view.focus();
}

export async function cutEditable(snap: EditableSnapshot): Promise<void> {
  if (snap.readOnly || !editableHasSelection(snap)) return;
  const text = selectedTextFromSnapshot(snap);
  await copyTextToClipboard(text);
  if (snap.el instanceof HTMLInputElement || snap.el instanceof HTMLTextAreaElement) {
    replaceInputSelection(snap.el, snap.start, snap.end, "", "deleteByCut");
    return;
  }
  if (snap.el instanceof HTMLElement) {
    const view = EditorView.findFromDOM(snap.el);
    if (view) {
      replaceCmSelection(view, snap.start, snap.end, "");
      return;
    }
  }
  replaceContentEditableSelection(snap.el, snap.range, "");
}

export async function copyEditable(snap: EditableSnapshot): Promise<void> {
  const text = selectedTextFromSnapshot(snap);
  if (text.length === 0) return;
  await copyTextToClipboard(text);
}

export async function pasteEditable(snap: EditableSnapshot): Promise<void> {
  if (snap.readOnly) return;
  if (tauriPlatform() === "macos") {
    // The system paste reads the pasteboard on the person's behalf and fires
    // a real paste event, so images and rich text reach the editor's handler.
    restoreEditableSelection(snap);
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("paste_into_focused_view");
    return;
  }
  const read = await readClipboardText();
  // A clipboard we could not read has nothing to paste.
  if (!read.readable || read.text.length === 0) return;
  const text = read.text;
  if (snap.el instanceof HTMLInputElement || snap.el instanceof HTMLTextAreaElement) {
    replaceInputSelection(snap.el, snap.start, snap.end, text, "insertFromPaste");
    return;
  }
  if (snap.el instanceof HTMLElement) {
    const view = EditorView.findFromDOM(snap.el);
    if (view) {
      replaceCmSelection(view, snap.start, snap.end, text);
      return;
    }
  }
  replaceContentEditableSelection(snap.el, snap.range, text);
}

/** Puts focus and the captured selection back before a system edit action. */
function restoreEditableSelection(snap: EditableSnapshot): void {
  const el = snap.el;
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    el.focus();
    el.setSelectionRange(snap.start, snap.end);
    return;
  }
  const view = EditorView.findFromDOM(el);
  if (view) {
    view.dispatch({ selection: EditorSelection.range(snap.start, snap.end) });
    view.focus();
    return;
  }
  el.focus();
  const sel = window.getSelection();
  if (sel && snap.range) {
    sel.removeAllRanges();
    sel.addRange(snap.range);
  }
}

export function selectAllEditable(snap: EditableSnapshot): void {
  const el = snap.el;
  // Editor state controls selection for custom editor surfaces.
  if (el instanceof HTMLElement) {
    const view = EditorView.findFromDOM(el);
    if (view) {
      cmSelectAll(view);
      view.focus();
      return;
    }
  }
  el.focus();
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    try {
      el.select();
    } catch {
      el.setSelectionRange(0, el.value.length);
    }
    return;
  }
  const range = document.createRange();
  range.selectNodeContents(el);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
}

export async function copySelectionText(text: string): Promise<void> {
  if (text.length === 0) return;
  await copyTextToClipboard(text);
}

/**
 * Expand the selection to the largest contiguous selectable ancestor under
 * the contextmenu target (stops at chrome `user-select: none`).
 */
export function selectAllAroundTarget(target: EventTarget | null): void {
  if (typeof window === "undefined") return;
  if (!(target instanceof Node)) return;
  let el: Element | null =
    target instanceof Element ? target : target.parentElement;
  let best: Element | null = null;
  while (el && el !== document.body && el !== document.documentElement) {
    try {
      if (window.getComputedStyle(el).userSelect === "none") break;
      best = el;
    } catch {
      break;
    }
    el = el.parentElement;
  }
  if (!best) return;
  const range = document.createRange();
  range.selectNodeContents(best);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
}
