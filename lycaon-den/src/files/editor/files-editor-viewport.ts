import { filesBufferText } from "../documents/project-files-buffers.ts";
import { acquireEditorViewport } from "../../components/source/editor/editor-viewport-pool.ts";
import { setEditorScrollTop } from "../../components/source/editor/editor-scroll-position.ts";
import {
  filesCollaborationExtension,
  rebindFilesCollaboration,
  canRestoreFilesCollaborationView,
} from "./files-editor-collaboration.ts";
import { editorReplica } from "../documents/editor-document.ts";
import { Compartment, Transaction } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { createEffect, createMemo, on, onCleanup, untrack } from "solid-js";
import { shortcutOverrides } from "../../settings/system/shortcut-prefs.ts";
import { modPressed } from "../../shortcuts/platform.ts";
import { languageExtensionForPath } from "../../components/source/editor/codemirror-lang.ts";
import {
  applySourceEditorEditable,
  createSourceEditorState,
  sourceUsesSimplifiedDisplay,
  type SourceEditorHandlers,
} from "../../components/source/editor/codemirror-theme.ts";
import { applyBufferReveal, hasPendingReveal } from "./files-editor-reveal.ts";
import { restoreEditorScrollTop } from "../../components/source/editor/editor-view-state.ts";
import { lineGutterExtension } from "../../components/source/annotations/line-gutter.ts";
import { noteBufferTyped } from "../documents/buffer-typing.ts";
import { scheduleEditorDocumentDraft } from "../documents/editor-document.ts";
import { captureBufferViewState, tryRestoreBufferViewState } from "./editor-session-fidelity.ts";
import { flushFilesDraftSyncFor, scheduleFilesDraftSync } from "../documents/files-draft-sync.ts";
import { scheduleEditorChrome } from "./files-editor-attach.ts";
import { currentDisplayPrefs } from "./files-editor-display.ts";
import {
  beginFilesEditorAttach,
  completeFilesEditorAttach,
  destroyFilesEditor,
  markFilesEditorViewDocDirty,
  notifyFilesEditorViewChanged,
  settleFilesEditorView,
  softDetachFilesEditor,
} from "./files-editor-host.ts";
import { filesGutterContextExtension } from "./files-gutter-context.ts";
import { hunkPlaybackExtension } from "../walk/hunk-playback.ts";
import { JUMP_CURSOR_LINE_THRESHOLD } from "../history/jump-history.ts";
import {
  applyFilesBufferDraft,
  clearFilesBufferReveal,
  markFilesBufferTyping,
} from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";

const filesHostExtensions = new Compartment();

type ViewportDependencies = Pick<FilesEditorScope, "projectId" | "buffer" | "live" | "currentView" | "editorView" |
  "historicalActive"> & {
  editable: () => boolean;
  hostElement: () => HTMLDivElement | undefined;
  bindView: (view: EditorView | undefined) => void;
  currentAbsent: () => boolean;
  reportCursor: (line: number, col: number) => void;
  onCursorTeleport: (line: number) => void;
  onGoToDefinitionAt: (position: number) => void;
  setSimplifiedDisplay: (simplified: boolean) => void;
  setChromeReady: (ready: boolean) => void;
  installChrome: (view: EditorView, path: string, prefs: ReturnType<typeof currentDisplayPrefs>) => void;
};

export function createFilesEditorViewport({ projectId: currentProjectId, buffer, editable, live, hostElement,
  currentView, editorView, bindView, historicalActive, currentAbsent, reportCursor,
  onCursorTeleport, onGoToDefinitionAt, setSimplifiedDisplay, setChromeReady, installChrome }: ViewportDependencies) {
  const binding = { get view() { return currentView(); } };
  let lastCursorLine = 1;
  let lastCursorCol = 1;
  const applyEditable = (v: EditorView, canEdit: boolean) =>
    applySourceEditorEditable(v, {
      editable: canEdit,
      collaborative: !!editorReplica(buffer().documentId),
      commandBridge: true,
    });

  const editorReady = createMemo(() => !buffer().loadError
    && buffer().content.state !== "suspended" && buffer().content.state !== "unloaded"
    && (buffer().baseSha256 != null || !!filesBufferText(buffer())));

  // Buffer identity controls view lifetime; editability updates separately.
  createEffect(on([
    () => currentProjectId(),
    () => buffer().key,
    () => buffer().path,
    editorReady,
    live,
  ], () => {
    if (!live()) return;
    const key = buffer().key;
    const path = buffer().path;
    if (buffer().loadError) return;
    if (buffer().baseSha256 == null && !filesBufferText(buffer())) return;
    const el = hostElement();
    if (!el) return;
    const canEdit = untrack(() => editable());
    const draft = untrack(() => filesBufferText(buffer()));
    const prefs = untrack(() => currentDisplayPrefs(buffer()));
    const projectId = currentProjectId();

    const wireHandlers = (handlers: SourceEditorHandlers) => {
      handlers.onDocChange = (update) => {
        // Remote transactions leave typing state unchanged.
        const typed = update.transactions.some(
          (tr) => tr.annotation(Transaction.userEvent) != null,
        );
        if (typed) {
          // The cached draft stays dirty until the debounce flushes.
          markFilesEditorViewDocDirty(projectId, key);
          noteBufferTyped(projectId, key);
          markFilesBufferTyping(projectId, key);
        }
        scheduleFilesDraftSync(projectId, key, () => {
          const text = binding.view?.state.doc.toString();
          if (text == null) return;
          settleFilesEditorView(projectId, key);
          applyFilesBufferDraft(projectId, key, text);
          if (typed) {
            const buffer = projectFilesState(projectId).byKey[key];
            if (buffer) {
              scheduleEditorDocumentDraft(projectId, buffer);
            }
          }
        });
      };
      handlers.onCursorChange = (line, col) => {
        notifyFilesEditorViewChanged();
        if (line === lastCursorLine && col === lastCursorCol) return;
        if (Math.abs(line - lastCursorLine) > JUMP_CURSOR_LINE_THRESHOLD) {
          onCursorTeleport(line);
        }
        lastCursorLine = line;
        lastCursorCol = col;
        reportCursor(line, col);
      };
    };

    const plan = beginFilesEditorAttach({
      projectId,
      key,
      path,
      draft,
      host: el,
    });
    wireHandlers(plan.handlers);

    if (plan.kind === "reused") {
      const reused = plan.view;
      bindView(reused);
      const replica = editorReplica(buffer().documentId);
      if (replica) rebindFilesCollaboration(reused, replica);
      applyEditable(reused, canEdit);
      setSimplifiedDisplay(sourceUsesSimplifiedDisplay(reused.state));
      if (plan.scrollTop > 0) {
        setEditorScrollTop(reused, plan.scrollTop);
        restoreEditorScrollTop(reused, plan.scrollTop);
      }
      const reusedHead = reused.state.selection.main.head;
      const reusedLine = reused.state.doc.lineAt(reusedHead);
      lastCursorLine = reusedLine.number;
      lastCursorCol = reusedHead - reusedLine.from + 1;
    } else {
      const replica = editorReplica(buffer().documentId);
      const hostExtensions = [
        filesCollaborationExtension(replica),
        filesGutterContextExtension,
        hunkPlaybackExtension,
        lineGutterExtension,
        EditorView.cursorScrollMargin.of({ x: 24, y: 5 }),
        EditorView.domEventHandlers({
          mousedown(event, cmView) {
            const mod = modPressed(event);
            if (!mod || event.button !== 0 || event.altKey || event.shiftKey) {
              return false;
            }
            const pos = cmView.posAtCoords({
              x: event.clientX,
              y: event.clientY,
            });
            if (pos == null) return false;
            event.preventDefault();
            onGoToDefinitionAt(pos);
            return true;
          },
        }),
      ];
      const state = createSourceEditorState({
            surface: "files",
            doc: draft,
            language: languageExtensionForPath(path),
            wordWrap: prefs.wordWrap,
            lineNumbers: prefs.lineNumbers,
            fontSize: prefs.fontSize,
            fontFamily: prefs.fontFamily,
            lineHeight: prefs.lineHeight,
            indentGuides: prefs.indentGuides,
            whitespace: prefs.whitespace,
            scrollbarTicks: prefs.scrollbarTicks,
            indent: prefs.indent,
            editable: canEdit,
            collaborative: !!replica,
            commandBridge: true,
            shortcutOverrides: untrack(() => shortcutOverrides()),
            extensions: filesHostExtensions.of(hostExtensions),
            handlers: plan.handlers,
          });
      setSimplifiedDisplay(sourceUsesSimplifiedDisplay(state));
      const selection = replica?.history.state.selection;
      const created = acquireEditorViewport(
        selection ? state.update({ selection, annotations: Transaction.addToHistory.of(false) }).state : state,
        el,
      );
      bindView(created);
      // Reattachment applies the current editability.
      applyEditable(created, canEdit);
      completeFilesEditorAttach({
        projectId,
        key,
        path,
        view: created,
        handlers: plan.handlers,
        host: el,
      });
      const attached = created;
      untrack(() => { tryRestoreBufferViewState(buffer(), attached); });
      const attachedHead = attached.state.selection.main.head;
      const attachedLine = attached.state.doc.lineAt(attachedHead);
      lastCursorLine = attachedLine.number;
      lastCursorCol = attachedHead - attachedLine.from + 1;
    }

    const revealView = binding.view;
    if (revealView && untrack(() => hasPendingReveal(buffer()) && !buffer().loading && !historicalActive() && !currentAbsent())) {
      untrack(() => applyBufferReveal(revealView, buffer()));
      clearFilesBufferReveal(projectId, key);
    }
    if (binding.view) {
      const head = binding.view.state.selection.main.head;
      const lineObj = binding.view.state.doc.lineAt(head);
      reportCursor(lineObj.number, head - lineObj.from + 1);
    }
    let cancelChrome: (() => void) | undefined;
    if (binding.view) {
      const live = binding.view;
      if (plan.kind === "reused") {
        installChrome(live, path, prefs);
        setChromeReady(true);
      } else {
        cancelChrome = scheduleEditorChrome(() => {
          installChrome(live, path, prefs);
          setChromeReady(true);
        }, el);
      }
    }
    onCleanup(() => {
      cancelChrome?.();
      setChromeReady(false);
      flushFilesDraftSyncFor(projectId, key);
      const closing = binding.view;
      bindView(undefined);
      if (!closing) return;
      const files = projectFilesState(projectId);
      const buf = files.byKey[key];
      // Open tabs retain and reparent their live editor.
      if (buf && !buf.loading) {
        // Captured positions survive process restarts.
        if (files.activeKey !== key) captureBufferViewState(buf, closing);
        softDetachFilesEditor(projectId, key, el);
        return;
      }
      destroyFilesEditor(projectId, key, { dropHandlers: true });
    });
  }));

  // Joining rebinds the document while preserving editor chrome.
  createEffect(() => {
    const replica = editorReplica(buffer().documentId);
    const current = editorView();
    if (!current) return;
    untrack(() => {
      const restore = replica && canRestoreFilesCollaborationView(current);
      if (rebindFilesCollaboration(current, replica)) {
        applyEditable(current, editable());
        if (restore) tryRestoreBufferViewState(buffer(), current);
      }
    });
  });

  // Editability changes preserve the live caret, selection, and scroll.
  createEffect(
    on(
      () => editable(),
      (canEdit) => {
        const v = binding.view;
        if (v) applyEditable(v, canEdit);
      },
      { defer: true },
    ),
  );

}
