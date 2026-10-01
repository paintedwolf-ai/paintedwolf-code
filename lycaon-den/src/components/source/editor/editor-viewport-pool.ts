import { closeCompletion } from "@codemirror/autocomplete";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { denScrollbars } from "./codemirror-scrollbars.ts";
import { editorCspNonce } from "./editor-csp-nonce.ts";

const IDLE_VIEWPORTS = 2;
const idle: EditorView[] = [];

/** Reuses empty viewports between resident documents. */
export function acquireEditorViewport(state: EditorState, host: HTMLElement): EditorView {
  const view = idle.pop();
  if (!view) return new EditorView({ state, parent: host });
  host.appendChild(view.dom);
  // Resetting an empty viewport avoids measuring the incoming document.
  view.scrollDOM.scrollTop = 0;
  view.scrollDOM.scrollLeft = 0;
  view.setState(state);
  view.requestMeasure();
  return view;
}

/** Pooled viewports retain no text, undo history, or component callbacks. */
export function releaseEditorViewport(view: EditorView): void {
  closeCompletion(view);
  view.dom.remove();
  if (idle.length >= IDLE_VIEWPORTS) {
    view.destroy();
    return;
  }
  view.setState(EditorState.create({ extensions: [editorCspNonce(), denScrollbars, EditorView.editable.of(false)] }));
  idle.push(view);
}

export function clearEditorViewportPool(): void {
  for (const view of idle.splice(0)) view.destroy();
}
