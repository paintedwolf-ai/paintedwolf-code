import { applyEditorEditingStyle } from "../../components/source/editor/editor-editing-style.ts";
import { editorKeymapPref, editorTabMovesFocusPref } from "../../settings/editor/editor-prefs.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import type { SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { editorReplica } from "../documents/editor-document.ts";
import { EditorView } from "@codemirror/view";
import { createEffect, onCleanup, untrack } from "solid-js";
import { shortcutOverrides } from "../../settings/system/shortcut-prefs.ts";
import { applyEditorCommandBridge, applyEditorDisplayPrefs } from "../../components/source/editor/codemirror-theme.ts";
import { applyBufferReveal, hasPendingReveal } from "./files-editor-reveal.ts";
import { setLineGutterRestoreHandler } from "../../components/source/annotations/line-gutter.ts";
import { setScopeDeletedLines } from "../../components/source/diff/scope-diff.ts";
import { getDeletedLines } from "../review/review-pane.ts";
import { setSecretScreen } from "../../components/source/secrets/secret-span-decorations.ts";
import { currentDisplayPrefs } from "./files-editor-display.ts";
import { getFilesEditorView, getFilesEditorViewDoc, isFilesEditorViewDocDirty, settleFilesEditorView } from "./files-editor-host.ts";
import { clearFilesEditorSecretScreen, filesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import type { DiffLineHunk } from "../review/line-diff.ts";
import { clearFilesBufferReveal } from "../documents/project-files-buffers.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";

export function observeEditorDocumentPresentation(options: Pick<FilesEditorScope, "projectId" | "buffer" | "currentView" | "editorView" | "chromeReady"> & {
  activeReaderHandle: () => SourceReaderHandle | undefined;
  activeEditorView: () => EditorView | undefined;
}) {
  createEffect(() => {
    if (editorReplica(options.buffer().documentId)) return;
    const draft = filesBufferText(options.buffer());
    const v = options.currentView();
    const known = getFilesEditorViewDoc(options.projectId(), options.buffer().key);
    if (!v || draft === known) return;
    if (isFilesEditorViewDocDirty(options.projectId(), options.buffer().key)) return;
    if (options.buffer().loadError) return;
    const live = v.state.doc.toString();
    if (draft === live) {
      settleFilesEditorView(options.projectId(), options.buffer().key);
      return;
    }
    const head = Math.min(v.state.selection.main.head, draft.length);
    settleFilesEditorView(options.projectId(), options.buffer().key);
    v.dispatch({
      changes: { from: 0, to: v.state.doc.length, insert: draft },
      selection: { anchor: head },
    });
  });

  // Reveal targets are consumed once.
  createEffect(() => {
    if (options.buffer().loading) return;
    const reveal = options.buffer().revealLine;
    const revealCol = options.buffer().revealColumn;
    const revealFocus = options.buffer().revealFocus;
    const reader = options.activeReaderHandle();
    const v = options.activeEditorView();
    if (!hasPendingReveal(options.buffer())) return;
    if (reader) {
      const key = untrack(() => options.buffer().key);
      if (reveal == null) reader.focus();
      else {
        void reader.revealLine(reveal, revealCol ?? undefined).then(() => {
          // Delayed content takes focus only if it remains unclaimed.
          if (revealFocus && (!document.activeElement || document.activeElement === document.body)) reader.focus();
        });
      }
      clearFilesBufferReveal(options.projectId(), key);
      return;
    }
    if (!v) return;
    applyBufferReveal(v, options.buffer());
    clearFilesBufferReveal(
      options.projectId(),
      untrack(() => options.buffer().key),
    );
  });

  createEffect(() => {
    const prefs = currentDisplayPrefs(options.buffer());
    const view = options.currentView();
    if (view && options.chromeReady()) {
      applyEditorDisplayPrefs(view, prefs);
    }
  });

  createEffect(() => {
    const style = editorKeymapPref(), tabFocus = editorTabMovesFocusPref();
    const view = options.editorView();
    if (view) applyEditorEditingStyle(view, style, tabFocus);
  });

  createEffect(() => {
    const overrides = shortcutOverrides();
    const view = options.currentView();
    if (view) applyEditorCommandBridge(view, overrides);
  });

}

export function observeEditorDocumentMarks(options: Pick<FilesEditorScope, "projectId" | "buffer" | "editorView"> & {
  rejectHunk: () => ((hunk: DiffLineHunk) => void) | undefined;
  deletedLines: () => ReturnType<typeof getDeletedLines>;
}) {
  // Hunk rejection uses the saved file and waits for unsaved edits.
  createEffect(() => {
    const v = options.editorView();
    if (!v) return;
    const reject = options.rejectHunk();
    setLineGutterRestoreHandler(v, !options.buffer().dirty && reject ? { onReject: reject } : null);
  });

  createEffect(() => {
    const v = options.editorView();
    if (v) setScopeDeletedLines(v, options.deletedLines());
  });

  // Host screens replace secret marks; edits map their existing ranges.
  createEffect(() => {
    const screen = filesEditorSecretScreen(options.buffer().key);
    const view = getFilesEditorView(options.projectId(), options.buffer().key);
    if (!view) return;
    setSecretScreen(view, screen ?? null);
  });
  onCleanup(() => clearFilesEditorSecretScreen(options.buffer().key));

}
