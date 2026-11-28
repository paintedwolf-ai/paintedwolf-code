import { createEffect, onCleanup, untrack, type Accessor } from "solid-js";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { applyEditorDisplayPrefs, createSourceEditorState } from "../editor/codemirror-theme.ts";
import { editorDisplayPrefs } from "../editor/editor-display-prefs.ts";
import type { EditorSurface } from "../editor/editor-scroll-lifecycle.ts";
import { applyPrettyConfig, createPrettyExtension } from "../pretty/pretty-layout.ts";
import type { PrettyConfig } from "../pretty/pretty-rescan.ts";

type Args = {
  /** Where the editor mounts; it is created once this exists. */
  parent: Accessor<HTMLElement | undefined>;
  surface: EditorSurface;
  text: Accessor<string>;
  language?: Accessor<Extension | null | undefined>;
  pretty: Accessor<PrettyConfig>;
  /** Decorations derived from the text; they change in the same update as the text. */
  marks?: Accessor<Extension>;
  label?: string;
  commandBridge?: boolean;
  /** Runs once the editor exists; what it returns runs before the editor is destroyed. */
  onEditor?: (view: EditorView) => (() => void) | void;
};

/**
 * A read-only editor over text held in memory. It follows the editor display
 * settings and Pretty, and replaces only the part of the text that changed.
 */
export function createReadonlyTextEditor(args: Args): Accessor<EditorView | undefined> {
  let editor: EditorView | undefined;
  const marks = new Compartment();
  createEffect(() => {
    const host = args.parent(); if (!host) return;
    const value = untrack(args.text);
    const view = new EditorView({ parent: host, state: createSourceEditorState({
      surface: args.surface, doc: value, language: untrack(() => args.language?.()) ?? undefined, label: args.label,
      ...untrack(editorDisplayPrefs), editable: false, scrollPastEnd: false, commandBridge: args.commandBridge,
      extensions: [
        EditorState.lineSeparator.of("\n"),
        marks.of(untrack(() => args.marks?.()) ?? []),
        createPrettyExtension(untrack(args.pretty)),
      ],
    }) });
    editor = view;
    const release = untrack(() => args.onEditor?.(view));
    onCleanup(() => { release?.(); view.destroy(); if (editor === view) editor = undefined; });
  });
  createEffect(() => {
    const value = args.text(), view = editor; if (!view) return;
    const effects = [marks.reconfigure(args.marks?.() ?? [])];
    const previous = view.state.doc.toString();
    if (previous === value) view.dispatch({ effects });
    else {
      let from = 0, to = previous.length, end = value.length;
      while (from < to && from < end && previous[from] === value[from]) from++;
      while (to > from && end > from && previous[to - 1] === value[end - 1]) { to--; end--; }
      view.dispatch({ changes: { from, to, insert: value.slice(from, end) }, effects });
    }
    applyPrettyConfig(view, args.pretty());
  });
  createEffect(() => { const prefs = editorDisplayPrefs(); if (editor) applyEditorDisplayPrefs(editor, prefs); });
  return () => editor;
}
